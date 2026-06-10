package cmds

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/microsoft/typescript-go/internal/bundled"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/serve"
	"github.com/microsoft/typescript-go/internal/vfs"
	"github.com/microsoft/typescript-go/internal/vfs/vfstest"
)

func newServeTestFS(t *testing.T, files map[string]any) vfs.FS {
	t.Helper()
	if !bundled.Embedded {
		t.Skip("bundled files are not embedded")
	}
	if _, ok := files["/project/tsconfig.json"]; !ok {
		files["/project/tsconfig.json"] = `{"compilerOptions": {"strict": true, "target": "esnext"}}`
	}
	return bundled.WrapFS(vfstest.FromMap(files, true /*useCaseSensitiveFileNames*/))
}

// lineWriter forwards complete lines to a channel (captures the daemon's
// socket-path announcement without racing on a shared buffer).
type lineWriter struct {
	mu    sync.Mutex
	buf   strings.Builder
	lines chan string
}

func newLineWriter() *lineWriter { return &lineWriter{lines: make(chan string, 8)} }

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	for {
		s := w.buf.String()
		idx := strings.IndexByte(s, '\n')
		if idx < 0 {
			break
		}
		w.lines <- s[:idx]
		w.buf.Reset()
		w.buf.WriteString(s[idx+1:])
	}
	return len(p), nil
}

// shortTempSocket returns a socket path short enough for the unix sockaddr
// limit (~104 bytes on darwin); t.TempDir() paths can exceed it.
func shortTempSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "tsa")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "d.sock")
}

func runRegistered(t *testing.T, family string, name string, configure func(flags any), args []string) (any, error) {
	t.Helper()
	cmd, ok := cli.Lookup(family, name)
	if !ok {
		t.Fatalf("command %s %s not registered", family, name)
	}
	fs := flag.NewFlagSet(family+" "+name, flag.ContinueOnError)
	flags := cmd.Flags(fs)
	if configure != nil {
		configure(flags)
	}
	return cmd.Run(context.Background(), nil, flags, args)
}

func TestServeDaemonStatusStopOverSocket(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("unix sockets not available")
	}
	fs := newServeTestFS(t, map[string]any{"/project/src/a.ts": "export const n: number = 1;\n"})
	socketPath := shortTempSocket(t)
	stdout := newLineWriter()

	daemonDone := make(chan error, 1)
	go func() {
		_, err := runRegistered(t, "serve", "", func(flags any) {
			f := flags.(*serveDaemonFlags)
			f.socket = true
			f.socketPath = socketPath
			f.fs = fs
			f.cwd = "/project"
			f.singleThreaded = true
			f.stdout = stdout
			f.stderr = io.Discard
		}, nil)
		daemonDone <- err
	}()

	// The daemon announces the socket path on stdout once it is listening.
	select {
	case announced := <-stdout.lines:
		if announced != socketPath {
			t.Fatalf("announced socket path %q, want %q", announced, socketPath)
		}
	case err := <-daemonDone:
		t.Fatalf("daemon exited early: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("daemon did not announce its socket path")
	}

	// serve status round-trip.
	result, err := runRegistered(t, "serve", "status", func(flags any) {
		flags.(*serveClientFlags).socketPath = socketPath
	}, nil)
	if err != nil {
		t.Fatalf("serve status: %v", err)
	}
	var status map[string]any
	if err := json.Unmarshal(result.(json.RawMessage), &status); err != nil {
		t.Fatalf("status result: %v", err)
	}
	if status["configPath"] != "/project/tsconfig.json" {
		t.Errorf("status configPath = %v", status["configPath"])
	}

	// In text mode the admin reply renders as compact key: value lines (the
	// same output layer every command uses), never as a JSON dump.
	var textBuf strings.Builder
	if err := (&cli.Output{W: &textBuf, Format: cli.FormatText}).Write(result); err != nil {
		t.Fatalf("text render: %v", err)
	}
	text := textBuf.String()
	if !strings.Contains(text, "configPath: /project/tsconfig.json") {
		t.Errorf("text status missing key: value rendering:\n%s", text)
	}
	if strings.Contains(text, "schemaVersion") || strings.Contains(text, "{") {
		t.Errorf("text status must not be a JSON dump:\n%s", text)
	}

	// serve stop shuts the daemon down cleanly.
	result, err = runRegistered(t, "serve", "stop", func(flags any) {
		flags.(*serveClientFlags).socketPath = socketPath
	}, nil)
	if err != nil {
		t.Fatalf("serve stop: %v", err)
	}
	var stop map[string]any
	if err := json.Unmarshal(result.(json.RawMessage), &stop); err != nil {
		t.Fatalf("stop result: %v", err)
	}
	if stop["ok"] != true {
		t.Errorf("stop result = %v, want {ok:true}", stop)
	}

	select {
	case err := <-daemonDone:
		if err != nil {
			t.Errorf("daemon exited with %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not exit after serve stop")
	}
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Errorf("socket file %s not cleaned up (stat err: %v)", socketPath, err)
	}
}

func TestServeDaemonStdio(t *testing.T) {
	t.Parallel()
	fs := newServeTestFS(t, map[string]any{"/project/src/a.ts": "export const n: number = 1;\n"})

	inR, inW := io.Pipe()
	stdout := newLineWriter()
	daemonDone := make(chan error, 1)
	go func() {
		_, err := runRegistered(t, "serve", "", func(flags any) {
			f := flags.(*serveDaemonFlags)
			f.stdio = true
			f.fs = fs
			f.cwd = "/project"
			f.singleThreaded = true
			f.stdin = inR
			f.stdout = stdout
			f.stderr = io.Discard
		}, nil)
		daemonDone <- err
	}()

	if _, err := io.WriteString(inW, `{"jsonrpc":"2.0","id":1,"method":"session/status"}`+"\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	var resp serve.Response
	select {
	case line := <-stdout.lines:
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("response %q: %v", line, err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no response to session/status")
	}
	if resp.Error != nil || string(resp.ID) != "1" {
		t.Fatalf("status response = %+v", resp)
	}

	if _, err := io.WriteString(inW, `{"jsonrpc":"2.0","id":2,"method":"session/shutdown"}`+"\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case err := <-daemonDone:
		if err != nil {
			t.Errorf("stdio daemon exited with %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stdio daemon did not exit after shutdown")
	}
}

// startTestDaemon launches the serve daemon command on an in-memory FS and
// returns its socket path; the daemon is stopped at cleanup.
func startTestDaemon(t *testing.T, files map[string]any) string {
	t.Helper()
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("unix sockets not available")
	}
	fs := newServeTestFS(t, files)
	socketPath := shortTempSocket(t)
	stdout := newLineWriter()

	daemonDone := make(chan error, 1)
	go func() {
		_, err := runRegistered(t, "serve", "", func(flags any) {
			f := flags.(*serveDaemonFlags)
			f.socket = true
			f.socketPath = socketPath
			f.fs = fs
			f.cwd = "/project"
			f.singleThreaded = true
			f.stdout = stdout
			f.stderr = io.Discard
		}, nil)
		daemonDone <- err
	}()
	select {
	case announced := <-stdout.lines:
		if announced != socketPath {
			t.Fatalf("announced socket path %q, want %q", announced, socketPath)
		}
	case err := <-daemonDone:
		t.Fatalf("daemon exited early: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("daemon did not announce its socket path")
	}
	t.Cleanup(func() {
		_, _ = runRegistered(t, "serve", "stop", func(flags any) {
			flags.(*serveClientFlags).socketPath = socketPath
		}, nil)
		select {
		case <-daemonDone:
		case <-time.After(10 * time.Second):
			t.Error("daemon did not exit after serve stop")
		}
	})
	return socketPath
}

// decodeClientResult unmarshals a serve client command result (the unwrapped
// envelope json.RawMessage) into out.
func decodeClientResult(t *testing.T, result any, out any) {
	t.Helper()
	raw, ok := result.(json.RawMessage)
	if !ok {
		t.Fatalf("client result is %T, want json.RawMessage", result)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("unmarshal client result %s: %v", raw, err)
	}
}

func TestServeOverlaySnapshotReloadCLI(t *testing.T) {
	t.Parallel()
	const valid = "export const n: number = 1;\n"
	const broken = `export const n: number = "broken";`
	socketPath := startTestDaemon(t, map[string]any{"/project/src/a.ts": valid})
	withSocket := func(flags any) {
		switch f := flags.(type) {
		case *serveClientFlags:
			f.socketPath = socketPath
		case *serveOverlayFlags:
			f.socketPath = socketPath
		}
	}

	// overlay set from stdin.
	result, err := runRegistered(t, "serve", "overlay", func(flags any) {
		f := flags.(*serveOverlayFlags)
		f.socketPath = socketPath
		f.stdin = strings.NewReader(broken)
	}, []string{"set", "src/a.ts"})
	if err != nil {
		t.Fatalf("overlay set: %v", err)
	}
	var setRes map[string]any
	decodeClientResult(t, result, &setRes)
	if setRes["file"] != "/project/src/a.ts" || setRes["overlayCount"] != float64(1) {
		t.Errorf("overlay set result = %v", setRes)
	}

	// snapshot save the broken state.
	result, err = runRegistered(t, "serve", "snapshot", withSocket, []string{"save", "exp1"})
	if err != nil {
		t.Fatalf("snapshot save: %v", err)
	}
	var saveRes map[string]any
	decodeClientResult(t, result, &saveRes)
	if saveRes["name"] != "exp1" || saveRes["files"] != float64(1) {
		t.Errorf("snapshot save result = %v", saveRes)
	}

	// overlay set from --from file (back to valid content).
	fromPath := filepath.Join(t.TempDir(), "valid.ts")
	if err := os.WriteFile(fromPath, []byte(valid), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := runRegistered(t, "serve", "overlay", func(flags any) {
		f := flags.(*serveOverlayFlags)
		f.socketPath = socketPath
		f.from = fromPath
	}, []string{"set", "src/a.ts"}); err != nil {
		t.Fatalf("overlay set --from: %v", err)
	}

	// overlay list shows the valid-content byte count.
	result, err = runRegistered(t, "serve", "overlay", withSocket, []string{"list"})
	if err != nil {
		t.Fatalf("overlay list: %v", err)
	}
	var overlays []map[string]any
	decodeClientResult(t, result, &overlays)
	if len(overlays) != 1 || overlays[0]["bytes"] != float64(len(valid)) {
		t.Errorf("overlay list = %v, want 1 entry with %d bytes", overlays, len(valid))
	}

	// snapshot restore brings the broken overlay back.
	if _, err := runRegistered(t, "serve", "snapshot", withSocket, []string{"restore", "exp1"}); err != nil {
		t.Fatalf("snapshot restore: %v", err)
	}
	result, err = runRegistered(t, "serve", "overlay", withSocket, []string{"list"})
	if err != nil {
		t.Fatalf("overlay list after restore: %v", err)
	}
	overlays = nil
	decodeClientResult(t, result, &overlays)
	if len(overlays) != 1 || overlays[0]["bytes"] != float64(len(broken)) {
		t.Errorf("overlay list after restore = %v, want 1 entry with %d bytes", overlays, len(broken))
	}

	// snapshot list / drop round-trip.
	result, err = runRegistered(t, "serve", "snapshot", withSocket, []string{"list"})
	if err != nil {
		t.Fatalf("snapshot list: %v", err)
	}
	var snaps []map[string]any
	decodeClientResult(t, result, &snaps)
	if len(snaps) != 1 || snaps[0]["name"] != "exp1" {
		t.Errorf("snapshot list = %v, want [exp1]", snaps)
	}
	if _, err := runRegistered(t, "serve", "snapshot", withSocket, []string{"drop", "exp1"}); err != nil {
		t.Fatalf("snapshot drop: %v", err)
	}
	_, err = runRegistered(t, "serve", "snapshot", withSocket, []string{"restore", "exp1"})
	if err == nil || cli.ExitCode(err) != cli.ExitNotFound {
		t.Errorf("restore dropped snapshot: err = %v, want not-found", err)
	}

	// overlay drop + reload.
	if _, err := runRegistered(t, "serve", "overlay", withSocket, []string{"drop", "src/a.ts"}); err != nil {
		t.Fatalf("overlay drop: %v", err)
	}
	result, err = runRegistered(t, "serve", "reload", withSocket, nil)
	if err != nil {
		t.Fatalf("serve reload: %v", err)
	}
	var reload map[string]any
	decodeClientResult(t, result, &reload)
	if reload["programFiles"].(float64) < 1 {
		t.Errorf("reload result = %v", reload)
	}
}

func TestServeSnapshotOverlayUsageErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
	}{
		{"snapshot", nil},
		{"snapshot", []string{"save"}},
		{"snapshot", []string{"bogus", "x"}},
		{"snapshot", []string{"list", "extra"}},
		{"overlay", nil},
		{"overlay", []string{"set"}},
		{"overlay", []string{"drop"}},
		{"overlay", []string{"bogus"}},
	}
	for _, tc := range cases {
		_, err := runRegistered(t, "serve", tc.name, func(flags any) {
			// A bogus socket path ensures usage validation fires before any
			// connection attempt is relevant.
			switch f := flags.(type) {
			case *serveClientFlags:
				f.socketPath = "/nonexistent.sock"
			case *serveOverlayFlags:
				f.socketPath = "/nonexistent.sock"
			}
		}, tc.args)
		if err == nil || cli.ExitCode(err) != cli.ExitUsage {
			t.Errorf("serve %s %v: err = %v, want usage error", tc.name, tc.args, err)
		}
	}
}

func TestServeFlagValidation(t *testing.T) {
	t.Parallel()
	_, err := runRegistered(t, "serve", "", func(flags any) {
		f := flags.(*serveDaemonFlags)
		f.stdio = true
		f.socket = true
	}, nil)
	if err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Errorf("--stdio --socket: err = %v, want usage error", err)
	}

	_, err = runRegistered(t, "serve", "", nil, []string{"positional"})
	if err == nil || cli.ExitCode(err) != cli.ExitUsage {
		t.Errorf("positional arg: err = %v, want usage error", err)
	}
}

func TestServeStatusNoDaemon(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("unix sockets not available")
	}
	_, err := runRegistered(t, "serve", "status", func(flags any) {
		flags.(*serveClientFlags).socketPath = shortTempSocket(t)
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "cannot connect") {
		t.Errorf("err = %v, want connection failure", err)
	}
}

func TestDefaultSocketPathStable(t *testing.T) {
	t.Parallel()
	a := serve.DefaultSocketPath("/project/tsconfig.json")
	b := serve.DefaultSocketPath("/project/tsconfig.json")
	other := serve.DefaultSocketPath("/elsewhere/tsconfig.json")
	if a != b {
		t.Errorf("socket path not deterministic: %q vs %q", a, b)
	}
	if a == other {
		t.Errorf("socket path does not vary by config: %q", a)
	}
	if !strings.HasSuffix(a, ".sock") || !strings.Contains(filepath.Base(a), "tsagent-") {
		t.Errorf("unexpected socket path shape: %q", a)
	}
}
