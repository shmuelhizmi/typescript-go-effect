package serve

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/microsoft/typescript-go/internal/bundled"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/tspath"
	"github.com/microsoft/typescript-go/internal/vfs"
	"github.com/microsoft/typescript-go/internal/vfs/osvfs"
)

// ErrShutdown is returned by the serve loops after a session/shutdown
// request was answered; callers treat it as a clean exit.
var ErrShutdown = errors.New("tsagent serve: shutdown requested")

// maxStatFiles caps per-request invalidation stats: when the program has
// more files than this, node_modules files are not stat'd.
const maxStatFiles = 2000

// StatFunc reports the modification time of a file on the *base* (disk)
// file system. ok is false when the file does not exist. Tests inject fakes
// so in-memory file systems (whose mtimes never change) still exercise
// invalidation.
type StatFunc func(path string) (modTime time.Time, ok bool)

// SessionOptions configures a daemon session.
type SessionOptions struct {
	// Project is a tsconfig.json path or directory ("" = discover from Cwd).
	Project string
	// Cwd defaults to the process working directory.
	Cwd string
	// FS is the base file system (default: bundled-lib-wrapped OS FS).
	// Overlays are layered on top of it for program builds.
	FS vfs.FS
	// Stat overrides mtime probing (default: FS.Stat-based).
	Stat StatFunc
	// SingleThreaded forces single-threaded program builds (tests).
	SingleThreaded bool
}

// Session is the daemon's long-lived state: the base FS, the overlay store,
// the current Workspace, and the mtime snapshot used for invalidation.
// Session is not safe for concurrent use; Server serializes all access
// behind a single dispatch mutex (one request at a time globally, v1).
type Session struct {
	configPath     string
	cwd            string
	baseFS         vfs.FS
	statFn         StatFunc
	singleThreaded bool

	overlays      map[string]string // normalized absolute path → content
	overlaysDirty bool              // overlays changed since last build
	snapshots     map[string]overlaySnapshot

	ws        *core.Workspace
	mtimes    map[string]mtimeEntry // snapshot at last build
	rebuilds  int                   // builds after the initial one
	lastBuild time.Duration
	started   time.Time
}

type mtimeEntry struct {
	modTime time.Time
	exists  bool
}

// overlaySnapshot is a named deep copy of the overlay map (in-memory, daemon
// lifetime only) — lets an agent explore two edit strategies and compare
// `check` results between them (spec §4.10).
type overlaySnapshot struct {
	overlays  map[string]string
	createdAt time.Time
}

// NewSession resolves the project and builds the initial Workspace.
func NewSession(opts SessionOptions) (*Session, error) {
	fs := opts.FS
	if fs == nil {
		fs = bundled.WrapFS(osvfs.FS())
	}
	cwd := opts.Cwd
	if cwd == "" {
		osCwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("getting current directory: %w", err)
		}
		cwd = osCwd
	}
	cwd = tspath.NormalizePath(cwd)

	statFn := opts.Stat
	if statFn == nil {
		statFn = func(path string) (time.Time, bool) {
			info := fs.Stat(path)
			if info == nil {
				return time.Time{}, false
			}
			return info.ModTime(), true
		}
	}

	s := &Session{
		cwd:            cwd,
		baseFS:         fs,
		statFn:         statFn,
		singleThreaded: opts.SingleThreaded,
		overlays:       map[string]string{},
		snapshots:      map[string]overlaySnapshot{},
		started:        time.Now(),
	}
	configPath, err := ResolveConfigPath(fs, cwd, opts.Project)
	if err != nil {
		return nil, err
	}
	s.configPath = configPath
	if err := s.rebuild(); err != nil {
		return nil, err
	}
	s.rebuilds = 0 // the initial build is not a rebuild
	return s, nil
}

// ConfigPath returns the resolved tsconfig path the session is pinned to.
func (s *Session) ConfigPath() string { return s.configPath }

// rebuild constructs a fresh Workspace over the base FS plus current
// overlays and snapshots program-file mtimes. Full rebuild every time:
// correctness over cleverness (plan §1.11 allows this).
func (s *Session) rebuild() error {
	start := time.Now()
	fs := s.baseFS
	if len(s.overlays) > 0 {
		fs = core.NewOverlayFS(s.baseFS, maps.Clone(s.overlays), nil)
	}
	ws, err := core.NewWorkspace(core.Options{
		Project:        s.configPath,
		Cwd:            s.cwd,
		FS:             fs,
		SingleThreaded: s.singleThreaded,
	})
	if err != nil {
		return err
	}
	s.ws = ws
	s.lastBuild = time.Since(start)
	s.rebuilds++
	s.overlaysDirty = false
	s.snapshotMtimes()
	return nil
}

// trackedFiles returns the disk files whose mtimes drive invalidation: the
// tsconfig plus all program files (bundled lib files excluded; node_modules
// files excluded when the program exceeds maxStatFiles).
func (s *Session) trackedFiles() []string {
	src := s.ws.Program.SourceFiles()
	skipNodeModules := len(src) > maxStatFiles
	files := make([]string, 0, len(src)+1)
	files = append(files, s.configPath)
	for _, f := range src {
		name := f.FileName()
		if strings.HasPrefix(name, "bundled:") {
			continue
		}
		if skipNodeModules && strings.Contains(name, "/node_modules/") {
			continue
		}
		files = append(files, name)
	}
	return files
}

func (s *Session) snapshotMtimes() {
	files := s.trackedFiles()
	s.mtimes = make(map[string]mtimeEntry, len(files))
	for _, f := range files {
		t, ok := s.statFn(f)
		s.mtimes[f] = mtimeEntry{modTime: t, exists: ok}
	}
}

// dirty reports whether the next request needs a rebuild: overlays changed,
// or any tracked file's mtime/existence changed since the last build.
// Limitation (v1): files newly created on disk that the tsconfig would pick
// up are not detected until some tracked file changes or overlays change.
func (s *Session) dirty() bool {
	if s.overlaysDirty {
		return true
	}
	for f, prev := range s.mtimes {
		t, ok := s.statFn(f)
		if ok != prev.exists || !t.Equal(prev.modTime) {
			return true
		}
	}
	return false
}

// FreshWorkspace rebuilds if anything changed and returns the current
// Workspace. Called before every non-admin request.
func (s *Session) FreshWorkspace() (*core.Workspace, error) {
	if s.dirty() {
		if err := s.rebuild(); err != nil {
			return nil, fmt.Errorf("rebuilding program: %w", err)
		}
	}
	return s.ws, nil
}

// Reload forces a rebuild regardless of dirtiness (session/reload).
func (s *Session) Reload() error { return s.rebuild() }

// absOverlayPath resolves an overlay file argument: absolute paths are
// normalized, relative paths resolve against the project root (the form
// displayed in all command output).
func (s *Session) absOverlayPath(file string) string {
	return tspath.GetNormalizedAbsolutePath(file, s.ws.RootDir)
}

// SetOverlay stores in-memory content for a file and marks the session for
// rebuild on the next request.
func (s *Session) SetOverlay(file string, content string) string {
	abs := s.absOverlayPath(file)
	s.overlays[abs] = content
	s.overlaysDirty = true
	return abs
}

// DropOverlays removes overlays; unknown paths are ignored. Returns the
// absolute paths actually dropped.
func (s *Session) DropOverlays(files []string) []string {
	var dropped []string
	for _, file := range files {
		abs := s.absOverlayPath(file)
		if _, ok := s.overlays[abs]; ok {
			delete(s.overlays, abs)
			s.overlaysDirty = true
			dropped = append(dropped, abs)
		}
	}
	return dropped
}

// OverlayInfo describes one overlay in session/overlays/list.
type OverlayInfo struct {
	File  string `json:"file"`
	Bytes int    `json:"bytes"`
}

// ListOverlays returns the current overlays sorted by path.
func (s *Session) ListOverlays() []OverlayInfo {
	infos := make([]OverlayInfo, 0, len(s.overlays))
	for file, content := range s.overlays {
		infos = append(infos, OverlayInfo{File: file, Bytes: len(content)})
	}
	slices.SortFunc(infos, func(a, b OverlayInfo) int { return strings.Compare(a.File, b.File) })
	return infos
}

// SnapshotInfo describes one named overlay snapshot (session/snapshot/list,
// session/status).
type SnapshotInfo struct {
	Name      string    `json:"name"`
	Files     int       `json:"files"`
	CreatedAt time.Time `json:"createdAt"`
}

// SaveSnapshot deep-copies the current overlay map under name (overwriting
// any previous snapshot with the same name).
func (s *Session) SaveSnapshot(name string) SnapshotInfo {
	snap := overlaySnapshot{overlays: maps.Clone(s.overlays), createdAt: time.Now()}
	if snap.overlays == nil {
		snap.overlays = map[string]string{}
	}
	s.snapshots[name] = snap
	return SnapshotInfo{Name: name, Files: len(snap.overlays), CreatedAt: snap.createdAt}
}

// RestoreSnapshot replaces the overlay map wholesale with the named
// snapshot's copy and marks the session dirty so the next request rebuilds.
func (s *Session) RestoreSnapshot(name string) (SnapshotInfo, error) {
	snap, ok := s.snapshots[name]
	if !ok {
		return SnapshotInfo{}, cli.NotFoundErrorf("no snapshot named %q (session/snapshot/list)", name)
	}
	s.overlays = maps.Clone(snap.overlays)
	s.overlaysDirty = true
	return SnapshotInfo{Name: name, Files: len(snap.overlays), CreatedAt: snap.createdAt}, nil
}

// DropSnapshot removes a named snapshot.
func (s *Session) DropSnapshot(name string) error {
	if _, ok := s.snapshots[name]; !ok {
		return cli.NotFoundErrorf("no snapshot named %q (session/snapshot/list)", name)
	}
	delete(s.snapshots, name)
	return nil
}

// ListSnapshots returns all snapshots sorted by name.
func (s *Session) ListSnapshots() []SnapshotInfo {
	infos := make([]SnapshotInfo, 0, len(s.snapshots))
	for name, snap := range s.snapshots {
		infos = append(infos, SnapshotInfo{Name: name, Files: len(snap.overlays), CreatedAt: snap.createdAt})
	}
	slices.SortFunc(infos, func(a, b SnapshotInfo) int { return strings.Compare(a.Name, b.Name) })
	return infos
}

// mutationPolicy documents the v1 mutation rule (see Server.dispatch): a
// mutating invocation (refactor --apply, edit without --dry-run, check fix
// --apply, …) writes to the real FS through the overlay-wrapped ws.FS, so
// overlay contents would silently shadow the bytes just written — and worse,
// the edits would be computed against the OVERLAY content and then flushed
// over the disk file. Until per-file conflict tracking exists, every mutating
// command is refused over RPC whenever any overlays are present.
const mutationPolicy = "refused over RPC while session overlays are present (overlays would shadow disk writes); drop overlays first"

// invocationMutatesDisk reports whether a routed invocation would write
// through to the real file system, given its fully populated FlagSet. It is
// keyed on the two transaction-flag conventions every mutating command
// follows, so new commands are covered automatically:
//
//   - dry-run-by-default commands (the refactor family, check fix) define an
//     --apply flag and mutate only when it is set;
//   - apply-by-default commands (edit) define a --dry-run flag and mutate
//     unless it is set.
//
// Commands defining neither flag have no write path.
func invocationMutatesDisk(fs *flag.FlagSet) bool {
	if f := fs.Lookup("apply"); f != nil {
		return f.Value.String() == "true"
	}
	if f := fs.Lookup("dry-run"); f != nil {
		return f.Value.String() != "true"
	}
	return false
}

// Status is the session/status result.
type Status struct {
	ConfigPath     string         `json:"configPath"`
	UptimeSeconds  float64        `json:"uptimeSeconds"`
	ProgramFiles   int            `json:"programFiles"`
	OverlayCount   int            `json:"overlayCount"`
	Snapshots      []SnapshotInfo `json:"snapshots"`
	Rebuilds       int            `json:"rebuilds"`
	LastBuildMs    float64        `json:"lastBuildMs"`
	HeapAllocBytes uint64         `json:"heapAllocBytes"`
	HeapSysBytes   uint64         `json:"heapSysBytes"`
	MutationPolicy string         `json:"mutationPolicy"`
}

// Status reports daemon health (session/status).
func (s *Session) Status() Status {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	return Status{
		ConfigPath:     s.configPath,
		UptimeSeconds:  time.Since(s.started).Seconds(),
		ProgramFiles:   len(s.ws.Program.SourceFiles()),
		OverlayCount:   len(s.overlays),
		Snapshots:      s.ListSnapshots(),
		Rebuilds:       s.rebuilds,
		LastBuildMs:    float64(s.lastBuild) / float64(time.Millisecond),
		HeapAllocBytes: mem.HeapAlloc,
		HeapSysBytes:   mem.HeapSys,
		MutationPolicy: mutationPolicy,
	}
}

// ResolveConfigPath resolves a --project value (tsconfig path, directory, or
// "" for upward discovery from cwd) to a tsconfig path without building a
// program. Mirrors core's workspace bootstrap resolution; restated here so
// `serve status`/`serve stop` can compute the default socket path cheaply.
func ResolveConfigPath(fs vfs.FS, cwd string, project string) (string, error) {
	cwd = tspath.NormalizePath(cwd)
	if project == "" {
		for dir := cwd; ; {
			candidate := tspath.CombinePaths(dir, "tsconfig.json")
			if fs.FileExists(candidate) {
				return candidate, nil
			}
			parent := tspath.GetDirectoryPath(dir)
			if parent == dir {
				return "", fmt.Errorf("no tsconfig.json found walking up from %s (use --project)", cwd)
			}
			dir = parent
		}
	}
	abs := tspath.GetNormalizedAbsolutePath(project, cwd)
	if fs.DirectoryExists(abs) {
		candidate := tspath.CombinePaths(abs, "tsconfig.json")
		if fs.FileExists(candidate) {
			return candidate, nil
		}
		return "", fmt.Errorf("no tsconfig.json in directory %s", abs)
	}
	if fs.FileExists(abs) {
		return abs, nil
	}
	return "", fmt.Errorf("project %s does not exist", abs)
}

// DefaultSocketPath derives the per-project socket path:
// $TMPDIR/tsagent-<sha1(configPath)[0:12]>.sock.
func DefaultSocketPath(configPath string) string {
	sum := sha1.Sum([]byte(configPath))
	return filepath.Join(os.TempDir(), "tsagent-"+hex.EncodeToString(sum[:])[:12]+".sock")
}

// ---------------------------------------------------------------------------
// Server

// Server dispatches ndjson JSON-RPC requests against a Session. One request
// is handled at a time globally (mu); connections are served sequentially.
type Server struct {
	mu      sync.Mutex
	session *Session
	logw    io.Writer // diagnostics only (stderr); never the protocol stream
}

// NewServer wraps a session. logw receives human-readable log lines (pass
// io.Discard to silence); it must not be the protocol writer.
func NewServer(session *Session, logw io.Writer) *Server {
	if logw == nil {
		logw = io.Discard
	}
	return &Server{session: session, logw: logw}
}

func (srv *Server) logf(format string, args ...any) {
	fmt.Fprintf(srv.logw, "tsagent serve: "+format+"\n", args...)
}

// ServeStream runs the ndjson request loop over r/w until EOF, context
// cancellation, or a shutdown request (which returns ErrShutdown after the
// response is written).
func (srv *Server) ServeStream(ctx context.Context, r io.Reader, w io.Writer) error {
	type readResult struct {
		line []byte
		err  error
	}
	lines := make(chan readResult)
	go func() {
		reader := bufio.NewReaderSize(r, 1<<20)
		for {
			line, err := reader.ReadBytes('\n')
			lines <- readResult{line: line, err: err}
			if err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case rr := <-lines:
			if len(bytes.TrimSpace(rr.line)) > 0 {
				resp, shutdown := srv.HandleLine(ctx, rr.line)
				if err := writeResponse(w, resp); err != nil {
					return err
				}
				if shutdown {
					return ErrShutdown
				}
			}
			if rr.err != nil {
				if rr.err == io.EOF {
					return nil
				}
				return rr.err
			}
		}
	}
}

func writeResponse(w io.Writer, resp *Response) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}

// Listen opens the unix socket listener (clearing any stale socket file
// first). Callers announce the path only after Listen returns, so clients
// never see an announced-but-not-yet-listening socket.
func Listen(path string) (net.Listener, error) {
	_ = os.Remove(path) // clear a stale socket from a dead daemon
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listening on %s: %w", path, err)
	}
	return ln, nil
}

// ServeSocket listens on a unix socket and serves connections sequentially
// until a shutdown request or context cancellation; the socket file is
// removed on exit.
func (srv *Server) ServeSocket(ctx context.Context, path string) error {
	ln, err := Listen(path)
	if err != nil {
		return err
	}
	return srv.Serve(ctx, ln, path)
}

// Serve accepts connections sequentially on ln until a shutdown request or
// context cancellation. The listener is closed and the socket file at path
// removed on exit.
func (srv *Server) Serve(ctx context.Context, ln net.Listener, path string) error {
	defer func() {
		ln.Close()
		os.Remove(path)
	}()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			ln.Close() // unblock Accept
		case <-done:
		}
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		err = srv.ServeStream(ctx, conn, conn)
		conn.Close()
		if errors.Is(err, ErrShutdown) {
			return nil
		}
		if err != nil && ctx.Err() == nil {
			srv.logf("connection error: %v", err)
		}
		if ctx.Err() != nil {
			return nil
		}
	}
}

// HandleLine parses one ndjson line and dispatches it. shutdown is true
// after a session/shutdown request (respond, then stop serving).
func (srv *Server) HandleLine(ctx context.Context, line []byte) (resp *Response, shutdown bool) {
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		return &Response{
			JSONRPC: "2.0",
			Error:   &RPCError{Code: CodeParseError, Message: fmt.Sprintf("parse error: %v", err)},
		}, false
	}
	return srv.Handle(ctx, &req)
}

// Handle dispatches one request under the global mutex.
func (srv *Server) Handle(ctx context.Context, req *Request) (resp *Response, shutdown bool) {
	srv.mu.Lock()
	defer srv.mu.Unlock()

	resp = &Response{JSONRPC: "2.0", ID: req.ID}
	defer func() {
		if r := recover(); r != nil {
			srv.logf("panic in %s: %v", req.Method, r)
			resp.Result = nil
			resp.Error = &RPCError{Code: CodeInternal, Message: fmt.Sprintf("internal panic: %v", r)}
			shutdown = false
		}
	}()

	if strings.HasPrefix(req.Method, "session/") {
		result, shutdown, err := srv.handleAdmin(req.Method, req.Params)
		if err != nil {
			resp.Error = errorFromExit(err, nil)
			return resp, false
		}
		raw, err := marshalResult(result)
		if err != nil {
			resp.Error = &RPCError{Code: CodeInternal, Message: fmt.Sprintf("marshaling result: %v", err)}
			return resp, false
		}
		resp.Result = raw
		return resp, shutdown
	}

	resp.Result, resp.Error = srv.dispatch(ctx, req)
	return resp, false
}

// dispatch routes a "<family>/<name>" (or bare "<family>" for
// single-command families like "check") method through the CLI registry.
func (srv *Server) dispatch(ctx context.Context, req *Request) (json.RawMessage, *RPCError) {
	var params Params
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: fmt.Sprintf("invalid params: %v (want {flags:{…}, args:[…]})", err)}
		}
	}

	format := cli.FormatJSON
	if params.Format != "" {
		parsed, err := cli.ParseFormat(params.Format)
		if err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
		format = parsed
	}

	family, name := req.Method, ""
	if slash := strings.IndexByte(req.Method, '/'); slash >= 0 {
		family, name = req.Method[:slash], req.Method[slash+1:]
	}
	if family == "serve" {
		return nil, &RPCError{Code: CodeMethodNotFound, Message: "serve commands are not available over RPC (use session/* admin methods)"}
	}
	cmd, ok := cli.Lookup(family, name)
	if !ok {
		return nil, &RPCError{Code: CodeMethodNotFound, Message: fmt.Sprintf("unknown method %q", req.Method)}
	}

	// Populate the command's flags struct exactly as the CLI would: register
	// flags on a FlagSet, then Set each param by name with its string form.
	fs := flag.NewFlagSet(req.Method, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var flagsStruct any
	if cmd.Flags != nil {
		flagsStruct = cmd.Flags(fs)
	}
	for _, k := range sortedKeys(params.Flags) {
		if err := fs.Set(k, flagValueString(params.Flags[k])); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: fmt.Sprintf("flag --%s: %v", k, err)}
		}
	}

	// v1 mutation policy: see mutationPolicy. The predicate is centralized on
	// the populated FlagSet, never per command, so every present and future
	// command with a write path is covered.
	if len(srv.session.overlays) > 0 && invocationMutatesDisk(fs) {
		return nil, &RPCError{Code: CodeRefused, Message: req.Method + " " + mutationPolicy + " (session/overlays/drop)"}
	}

	var ws *core.Workspace
	if cmd.NeedsProgram {
		fresh, err := srv.session.FreshWorkspace()
		if err != nil {
			return nil, &RPCError{Code: CodeInternal, Message: err.Error()}
		}
		ws = fresh
	}

	result, err := cmd.Run(ctx, ws, flagsStruct, params.Args)
	if err != nil {
		var data json.RawMessage
		if !isNilResult(result) {
			data, _ = renderResult(result, format, params.Limit, params.Offset)
		}
		return nil, errorFromExit(err, data)
	}
	raw, err := renderResult(result, format, params.Limit, params.Offset)
	if err != nil {
		return nil, &RPCError{Code: CodeInternal, Message: fmt.Sprintf("marshaling result: %v", err)}
	}
	return raw, nil
}

func (srv *Server) handleAdmin(method string, raw json.RawMessage) (result any, shutdown bool, err error) {
	s := srv.session
	switch method {
	case "session/status":
		return s.Status(), false, nil

	case "session/overlays/set":
		var p struct {
			File    string `json:"file"`
			Content string `json:"content"`
		}
		if err := unmarshalParams(raw, &p); err != nil {
			return nil, false, err
		}
		if p.File == "" {
			return nil, false, cli.UsageErrorf("session/overlays/set requires params {file, content}")
		}
		abs := s.SetOverlay(p.File, p.Content)
		return map[string]any{"file": abs, "overlayCount": len(s.overlays)}, false, nil

	case "session/overlays/drop":
		var p struct {
			Files []string `json:"files"`
		}
		if err := unmarshalParams(raw, &p); err != nil {
			return nil, false, err
		}
		if len(p.Files) == 0 {
			return nil, false, cli.UsageErrorf("session/overlays/drop requires params {files: […]}")
		}
		dropped := s.DropOverlays(p.Files)
		if dropped == nil {
			dropped = []string{}
		}
		return map[string]any{"dropped": dropped, "overlayCount": len(s.overlays)}, false, nil

	case "session/overlays/list":
		return s.ListOverlays(), false, nil

	case "session/snapshot/save":
		name, err := snapshotName(method, raw)
		if err != nil {
			return nil, false, err
		}
		return s.SaveSnapshot(name), false, nil

	case "session/snapshot/restore":
		name, err := snapshotName(method, raw)
		if err != nil {
			return nil, false, err
		}
		info, err := s.RestoreSnapshot(name)
		if err != nil {
			return nil, false, err
		}
		return info, false, nil

	case "session/snapshot/drop":
		name, err := snapshotName(method, raw)
		if err != nil {
			return nil, false, err
		}
		if err := s.DropSnapshot(name); err != nil {
			return nil, false, err
		}
		return map[string]any{"dropped": name, "snapshotCount": len(s.snapshots)}, false, nil

	case "session/snapshot/list":
		return s.ListSnapshots(), false, nil

	case "session/reload":
		if err := s.Reload(); err != nil {
			return nil, false, err
		}
		return map[string]any{
			"rebuilds":     s.rebuilds,
			"lastBuildMs":  float64(s.lastBuild) / float64(time.Millisecond),
			"programFiles": len(s.ws.Program.SourceFiles()),
		}, false, nil

	case "session/shutdown":
		return map[string]any{"ok": true}, true, nil
	}
	return nil, false, &RPCError{Code: CodeMethodNotFound, Message: fmt.Sprintf("unknown method %q", method)}
}

// snapshotName extracts the required {name} param of session/snapshot/*.
func snapshotName(method string, raw json.RawMessage) (string, error) {
	var p struct {
		Name string `json:"name"`
	}
	if err := unmarshalParams(raw, &p); err != nil {
		return "", err
	}
	if p.Name == "" {
		return "", cli.UsageErrorf("%s requires params {name}", method)
	}
	return p.Name, nil
}

func unmarshalParams(raw json.RawMessage, into any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return cli.UsageErrorf("invalid params: %v", err)
	}
	return nil
}

// marshalResult renders a handler result through the same output layer the
// CLI uses with --format json (envelope with schemaVersion; Lister windowing
// with no limit), so RPC results are byte-identical to CLI output.
func marshalResult(result any) (json.RawMessage, error) {
	return renderResult(result, cli.FormatJSON, 0, 0)
}

// renderResult renders a handler result through the CLI output layer in the
// requested format with --limit/--offset windowing. JSON results are returned
// as the structured envelope; text/ndjson results are returned as
// {"rendered": "<exact CLI output bytes>"} so a routing client (--connect)
// can print them verbatim, byte-identical to a local run.
func renderResult(result any, format cli.Format, limit int, offset int) (json.RawMessage, error) {
	var buf bytes.Buffer
	out := &cli.Output{W: &buf, Format: format, Limit: limit, Offset: offset}
	if err := out.Write(result); err != nil {
		return nil, err
	}
	if format == cli.FormatJSON {
		return json.RawMessage(bytes.TrimSpace(buf.Bytes())), nil
	}
	return json.Marshal(renderedResult{Rendered: buf.String()})
}

// renderedResult is the result shape for non-JSON server-side rendering.
type renderedResult struct {
	Rendered string `json:"rendered"`
}

// flagValueString renders a JSON flag value in the form flag.FlagSet.Set
// expects: booleans as "true"/"false", numbers without a float exponent.
func flagValueString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// isNilResult mirrors the CLI entry point's check for handlers that return
// a typed-nil result alongside an error.
func isNilResult(result any) bool {
	if result == nil {
		return true
	}
	v := reflect.ValueOf(result)
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice:
		return v.IsNil()
	}
	return false
}
