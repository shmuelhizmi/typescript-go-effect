package serve_test

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/microsoft/typescript-go/internal/bundled"
	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	_ "github.com/microsoft/typescript-go/internal/tsagent/cmds" // populate the command registry
	"github.com/microsoft/typescript-go/internal/tsagent/serve"
	"github.com/microsoft/typescript-go/internal/vfs"
	"github.com/microsoft/typescript-go/internal/vfs/vfstest"
)

// fakeStat is an injectable StatFunc for the in-memory FS: every file
// reports a version-counter mtime; bump() marks a file changed.
type fakeStat struct {
	mu       sync.Mutex
	versions map[string]int
}

func newFakeStat() *fakeStat { return &fakeStat{versions: map[string]int{}} }

func (f *fakeStat) bump(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.versions[path]++
}

func (f *fakeStat) fn(path string) (time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return time.Unix(int64(f.versions[path]), 0), true
}

// newTestSession builds a session over an in-memory FS rooted at /project.
func newTestSession(t *testing.T, files map[string]any, stat *fakeStat) (*serve.Session, vfs.FS) {
	t.Helper()
	if !bundled.Embedded {
		t.Skip("bundled files are not embedded")
	}
	if _, ok := files["/project/tsconfig.json"]; !ok {
		files["/project/tsconfig.json"] = `{"compilerOptions": {"strict": true, "target": "esnext"}}`
	}
	fs := bundled.WrapFS(vfstest.FromMap(files, true /*useCaseSensitiveFileNames*/))
	opts := serve.SessionOptions{
		Project:        "/project",
		Cwd:            "/project",
		FS:             fs,
		SingleThreaded: true,
	}
	if stat != nil {
		opts.Stat = stat.fn
	}
	session, err := serve.NewSession(opts)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return session, fs
}

// testClient drives a Server over an in-memory duplex (net.Pipe) speaking
// ndjson, exactly as a socket client would.
type testClient struct {
	t      *testing.T
	conn   net.Conn
	reader *bufio.Reader
	nextID int
}

func startServer(t *testing.T, session *serve.Session) *testClient {
	t.Helper()
	srvConn, cliConn := net.Pipe()
	server := serve.NewServer(session, io.Discard)
	go func() {
		_ = server.ServeStream(context.Background(), srvConn, srvConn)
		srvConn.Close()
	}()
	t.Cleanup(func() { cliConn.Close() })
	return &testClient{t: t, conn: cliConn, reader: bufio.NewReaderSize(cliConn, 1<<20)}
}

func (c *testClient) send(method string, params any) int {
	c.t.Helper()
	c.nextID++
	req := map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": method}
	if params != nil {
		req["params"] = params
	}
	data, err := json.Marshal(req)
	if err != nil {
		c.t.Fatalf("marshal request: %v", err)
	}
	if _, err := c.conn.Write(append(data, '\n')); err != nil {
		c.t.Fatalf("write request: %v", err)
	}
	return c.nextID
}

func (c *testClient) recv() serve.Response {
	c.t.Helper()
	line, err := c.reader.ReadBytes('\n')
	if err != nil {
		c.t.Fatalf("read response: %v", err)
	}
	var resp serve.Response
	if err := json.Unmarshal(line, &resp); err != nil {
		c.t.Fatalf("unmarshal response %q: %v", line, err)
	}
	return resp
}

func (c *testClient) call(method string, params any) serve.Response {
	c.t.Helper()
	id := c.send(method, params)
	resp := c.recv()
	if got := strings.TrimSpace(string(resp.ID)); got != fmt.Sprint(id) {
		c.t.Fatalf("response id = %s, want %d", got, id)
	}
	return resp
}

func (c *testClient) mustResult(method string, params any) json.RawMessage {
	c.t.Helper()
	resp := c.call(method, params)
	if resp.Error != nil {
		c.t.Fatalf("%s: unexpected error: %+v", method, resp.Error)
	}
	return resp.Result
}

func (c *testClient) mustError(method string, params any, wantCode int) *serve.RPCError {
	c.t.Helper()
	resp := c.call(method, params)
	if resp.Error == nil {
		c.t.Fatalf("%s: expected error code %d, got result %s", method, wantCode, resp.Result)
	}
	if resp.Error.Code != wantCode {
		c.t.Fatalf("%s: error code = %d (%s), want %d", method, resp.Error.Code, resp.Error.Message, wantCode)
	}
	return resp.Error
}

func decodeJSON(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return m
}

// checkItems runs `check` over RPC and returns the list envelope's items.
func checkItems(t *testing.T, c *testClient) []any {
	t.Helper()
	envelope := decodeJSON(t, c.mustResult("check", serve.Params{}))
	items, ok := envelope["items"].([]any)
	if !ok {
		t.Fatalf("check result has no items array: %v", envelope)
	}
	return items
}

const validA = `export const n: number = 1;
export function double(x: number): number { return x * 2; }
`

func TestOutlineMatchesDirect(t *testing.T) {
	t.Parallel()
	session, _ := newTestSession(t, map[string]any{"/project/src/a.ts": validA}, nil)
	c := startServer(t, session)

	raw := c.mustResult("map/outline", serve.Params{Args: []string{"src/a.ts"}})

	// Direct call through the same registry + output layer.
	cmd, ok := cli.Lookup("map", "outline")
	if !ok {
		t.Fatal("map outline not registered")
	}
	fs := flag.NewFlagSet("map outline", flag.ContinueOnError)
	flags := cmd.Flags(fs)
	ws, err := session.FreshWorkspace()
	if err != nil {
		t.Fatalf("FreshWorkspace: %v", err)
	}
	direct, err := cmd.Run(context.Background(), ws, flags, []string{"src/a.ts"})
	if err != nil {
		t.Fatalf("direct map outline: %v", err)
	}
	var buf strings.Builder
	if err := (&cli.Output{W: &buf, Format: cli.FormatJSON}).Write(direct); err != nil {
		t.Fatalf("marshal direct result: %v", err)
	}

	var fromRPC, fromDirect any
	if err := json.Unmarshal(raw, &fromRPC); err != nil {
		t.Fatalf("unmarshal RPC result: %v", err)
	}
	if err := json.Unmarshal([]byte(buf.String()), &fromDirect); err != nil {
		t.Fatalf("unmarshal direct result: %v", err)
	}
	if !reflect.DeepEqual(fromRPC, fromDirect) {
		t.Errorf("RPC result differs from direct call\nRPC:    %s\ndirect: %s", raw, buf.String())
	}
}

func TestOverlayRebuildRoundTrip(t *testing.T) {
	t.Parallel()
	session, _ := newTestSession(t, map[string]any{"/project/src/a.ts": validA}, nil)
	c := startServer(t, session)

	if items := checkItems(t, c); len(items) != 0 {
		t.Fatalf("clean project: check returned %d diagnostics: %v", len(items), items)
	}

	// Introduce a type error via overlay; check must see it (rebuild).
	setResult := decodeJSON(t, c.mustResult("session/overlays/set", map[string]any{
		"file":    "src/a.ts",
		"content": `export const n: number = "not a number";`,
	}))
	inner := decodeJSON(t, mustRaw(t, setResult["result"]))
	if inner["overlayCount"] != float64(1) {
		t.Errorf("overlays/set overlayCount = %v, want 1", inner["overlayCount"])
	}

	items := checkItems(t, c)
	if len(items) != 1 {
		t.Fatalf("after overlay: check returned %d diagnostics, want 1: %v", len(items), items)
	}
	diag := items[0].(map[string]any)
	if diag["code"] != "TS2322" {
		t.Errorf("diagnostic code = %v, want TS2322", diag["code"])
	}

	// Drop the overlay; the diagnostic must disappear (rebuild again).
	c.mustResult("session/overlays/drop", map[string]any{"files": []string{"src/a.ts"}})
	if items := checkItems(t, c); len(items) != 0 {
		t.Fatalf("after overlay drop: check returned %d diagnostics: %v", len(items), items)
	}

	// Two overlay changes → at least 2 rebuilds recorded.
	status := decodeJSON(t, mustRaw(t, decodeJSON(t, c.mustResult("session/status", nil))["result"]))
	if rebuilds := status["rebuilds"].(float64); rebuilds < 2 {
		t.Errorf("rebuilds = %v, want >= 2", rebuilds)
	}
}

// mustRaw re-marshals a decoded JSON value so nested envelopes can be
// decoded uniformly.
func mustRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	return raw
}

func TestOverlaysList(t *testing.T) {
	t.Parallel()
	session, _ := newTestSession(t, map[string]any{"/project/src/a.ts": validA}, nil)
	c := startServer(t, session)

	c.mustResult("session/overlays/set", map[string]any{"file": "src/a.ts", "content": "export {};"})
	envelope := decodeJSON(t, c.mustResult("session/overlays/list", nil))
	var list []map[string]any
	if err := json.Unmarshal(mustRaw(t, envelope["result"]), &list); err != nil {
		t.Fatalf("overlays/list result: %v", err)
	}
	if len(list) != 1 || list[0]["file"] != "/project/src/a.ts" || list[0]["bytes"] != float64(len("export {};")) {
		t.Errorf("overlays/list = %v", list)
	}
}

func TestStatusFields(t *testing.T) {
	t.Parallel()
	session, _ := newTestSession(t, map[string]any{"/project/src/a.ts": validA}, nil)
	c := startServer(t, session)

	envelope := decodeJSON(t, c.mustResult("session/status", nil))
	if envelope["schemaVersion"] != float64(1) {
		t.Errorf("schemaVersion = %v, want 1", envelope["schemaVersion"])
	}
	status := decodeJSON(t, mustRaw(t, envelope["result"]))
	if status["configPath"] != "/project/tsconfig.json" {
		t.Errorf("configPath = %v", status["configPath"])
	}
	if status["programFiles"].(float64) < 1 {
		t.Errorf("programFiles = %v, want >= 1", status["programFiles"])
	}
	if status["overlayCount"] != float64(0) {
		t.Errorf("overlayCount = %v, want 0", status["overlayCount"])
	}
	if status["rebuilds"] != float64(0) {
		t.Errorf("rebuilds = %v, want 0 (initial build is not a rebuild)", status["rebuilds"])
	}
	if status["heapAllocBytes"].(float64) <= 0 {
		t.Errorf("heapAllocBytes = %v, want > 0", status["heapAllocBytes"])
	}
	if status["uptimeSeconds"].(float64) < 0 {
		t.Errorf("uptimeSeconds = %v, want >= 0", status["uptimeSeconds"])
	}
	if policy, _ := status["refactorApplyPolicy"].(string); !strings.Contains(policy, "refused") {
		t.Errorf("refactorApplyPolicy = %q, want policy text", policy)
	}
}

func TestSessionReload(t *testing.T) {
	t.Parallel()
	session, _ := newTestSession(t, map[string]any{"/project/src/a.ts": validA}, nil)
	c := startServer(t, session)

	result := decodeJSON(t, mustRaw(t, decodeJSON(t, c.mustResult("session/reload", nil))["result"]))
	if result["rebuilds"] != float64(1) {
		t.Errorf("rebuilds after reload = %v, want 1", result["rebuilds"])
	}
	if result["programFiles"].(float64) < 1 {
		t.Errorf("programFiles = %v, want >= 1", result["programFiles"])
	}
}

func TestMtimeInvalidation(t *testing.T) {
	t.Parallel()
	stat := newFakeStat()
	session, fs := newTestSession(t, map[string]any{"/project/src/a.ts": validA}, stat)
	c := startServer(t, session)

	if items := checkItems(t, c); len(items) != 0 {
		t.Fatalf("clean project: %d diagnostics", len(items))
	}

	// Change the file on the base FS and bump its fake mtime.
	if err := fs.WriteFile("/project/src/a.ts", `export const n: number = "broken";`); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	stat.bump("/project/src/a.ts")

	items := checkItems(t, c)
	if len(items) != 1 {
		t.Fatalf("after disk edit: %d diagnostics, want 1: %v", len(items), items)
	}
}

func TestUnknownMethod(t *testing.T) {
	t.Parallel()
	session, _ := newTestSession(t, map[string]any{"/project/src/a.ts": validA}, nil)
	c := startServer(t, session)

	c.mustError("bogus/none", serve.Params{}, serve.CodeMethodNotFound)
	c.mustError("session/bogus", nil, serve.CodeMethodNotFound)
	c.mustError("serve/status", nil, serve.CodeMethodNotFound) // serve family is not RPC-dispatchable
}

func TestBadFlagInvalidParams(t *testing.T) {
	t.Parallel()
	session, _ := newTestSession(t, map[string]any{"/project/src/a.ts": validA}, nil)
	c := startServer(t, session)

	rpcErr := c.mustError("map/outline", serve.Params{
		Flags: map[string]any{"no-such-flag": true},
		Args:  []string{"src/a.ts"},
	}, serve.CodeInvalidParams)
	if !strings.Contains(rpcErr.Message, "no-such-flag") {
		t.Errorf("error message %q does not name the flag", rpcErr.Message)
	}

	// Bad admin params are invalid params too.
	c.mustError("session/overlays/set", map[string]any{"content": "x"}, serve.CodeInvalidParams)
}

func TestNotFoundError(t *testing.T) {
	t.Parallel()
	session, _ := newTestSession(t, map[string]any{"/project/src/a.ts": validA}, nil)
	c := startServer(t, session)

	c.mustError("map/outline", serve.Params{Args: []string{"src/missing.ts"}}, serve.CodeNotFound)
}

func TestRefactorApplyRefusedWithOverlays(t *testing.T) {
	t.Parallel()
	session, _ := newTestSession(t, map[string]any{"/project/src/a.ts": validA}, nil)
	c := startServer(t, session)

	c.mustResult("session/overlays/set", map[string]any{"file": "src/a.ts", "content": validA})
	rpcErr := c.mustError("refactor/rename", serve.Params{
		Flags: map[string]any{"apply": true, "name": "double"},
		Args:  []string{"newName"},
	}, serve.CodeRefused)
	if !strings.Contains(rpcErr.Message, "overlays") {
		t.Errorf("refusal message %q does not mention overlays", rpcErr.Message)
	}
}

func TestFlagValuesFromJSON(t *testing.T) {
	t.Parallel()
	session, _ := newTestSession(t, map[string]any{
		"/project/src/a.ts": "export const bad: number = \"x\";\nexport const meh: string = 1;\n",
	}, nil)
	c := startServer(t, session)

	// Bool and string flags populated from JSON values, mirroring CLI flags.
	envelope := decodeJSON(t, c.mustResult("check", serve.Params{
		Flags: map[string]any{"code": "TS2322", "suggestions": false},
	}))
	if envelope["total"].(float64) != 2 {
		t.Errorf("check --code TS2322 total = %v, want 2", envelope["total"])
	}
}

func TestOrderedIDs(t *testing.T) {
	t.Parallel()
	session, _ := newTestSession(t, map[string]any{"/project/src/a.ts": validA}, nil)
	c := startServer(t, session)

	// Pipeline three requests, then read three responses: ids must come back
	// in order (requests are handled sequentially).
	var ids []int
	done := make(chan struct{})
	go func() {
		defer close(done)
		ids = append(ids, c.send("session/status", nil))
		ids = append(ids, c.send("map/outline", serve.Params{Args: []string{"src/a.ts"}}))
		ids = append(ids, c.send("session/status", nil))
	}()
	var got []string
	for range 3 {
		resp := c.recv()
		got = append(got, strings.TrimSpace(string(resp.ID)))
	}
	<-done
	for i, id := range ids {
		if got[i] != fmt.Sprint(id) {
			t.Fatalf("response %d has id %s, want %d (got order %v)", i, got[i], id, got)
		}
	}
}

func TestParseError(t *testing.T) {
	t.Parallel()
	session, _ := newTestSession(t, map[string]any{"/project/src/a.ts": validA}, nil)
	c := startServer(t, session)

	if _, err := c.conn.Write([]byte("this is not json\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp := c.recv()
	if resp.Error == nil || resp.Error.Code != serve.CodeParseError {
		t.Fatalf("expected parse error, got %+v", resp)
	}
	if string(resp.ID) != "null" && len(resp.ID) != 0 {
		t.Errorf("parse error id = %q, want null", resp.ID)
	}
}

func TestShutdown(t *testing.T) {
	t.Parallel()
	session, _ := newTestSession(t, map[string]any{"/project/src/a.ts": validA}, nil)

	srvConn, cliConn := net.Pipe()
	server := serve.NewServer(session, io.Discard)
	done := make(chan error, 1)
	go func() { done <- server.ServeStream(context.Background(), srvConn, srvConn) }()
	c := &testClient{t: t, conn: cliConn, reader: bufio.NewReader(cliConn)}

	resp := c.call("session/shutdown", nil)
	if resp.Error != nil {
		t.Fatalf("shutdown error: %+v", resp.Error)
	}
	select {
	case err := <-done:
		if err != serve.ErrShutdown {
			t.Errorf("ServeStream returned %v, want ErrShutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeStream did not stop after shutdown")
	}
}

func TestSocketStatusRoundTrip(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("unix sockets not available")
	}
	session, _ := newTestSession(t, map[string]any{"/project/src/a.ts": validA}, nil)
	server := serve.NewServer(session, io.Discard)

	// Keep the path under the unix sockaddr limit (~104 bytes on darwin);
	// t.TempDir() paths can exceed it.
	dir, err := os.MkdirTemp("", "tsa")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "t.sock")
	ln, err := serve.Listen(path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background(), ln, path) }()

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c := &testClient{t: t, conn: conn, reader: bufio.NewReader(conn)}
	envelope := decodeJSON(t, c.mustResult("session/status", nil))
	status := decodeJSON(t, mustRaw(t, envelope["result"]))
	if status["configPath"] != "/project/tsconfig.json" {
		t.Errorf("configPath over socket = %v", status["configPath"])
	}
	c.mustResult("session/shutdown", nil)
	conn.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve returned %v, want nil after shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop after shutdown")
	}
}
