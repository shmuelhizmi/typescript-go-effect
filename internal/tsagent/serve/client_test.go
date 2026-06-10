package serve_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/tsagent/cli"
	"github.com/microsoft/typescript-go/internal/tsagent/serve"
)

// localOutput runs a registry command directly against the session's
// workspace and renders it with the local CLI output layer — the reference
// bytes that daemon-routed output must match exactly.
func localOutput(t *testing.T, session *serve.Session, family string, name string, args []string, format cli.Format) string {
	t.Helper()
	cmd, ok := cli.Lookup(family, name)
	if !ok {
		t.Fatalf("command %s %s not registered", family, name)
	}
	fs := flag.NewFlagSet(family+" "+name, flag.ContinueOnError)
	var flags any
	if cmd.Flags != nil {
		flags = cmd.Flags(fs)
	}
	ws, err := session.FreshWorkspace()
	if err != nil {
		t.Fatalf("FreshWorkspace: %v", err)
	}
	result, err := cmd.Run(context.Background(), ws, flags, args)
	if err != nil {
		t.Fatalf("direct %s %s: %v", family, name, err)
	}
	var buf bytes.Buffer
	if err := (&cli.Output{W: &buf, Format: format}).Write(result); err != nil {
		t.Fatalf("render direct result: %v", err)
	}
	return buf.String()
}

func TestServerSideTextRendering(t *testing.T) {
	t.Parallel()
	session, _ := newTestSession(t, map[string]any{"/project/src/a.ts": validA}, nil)
	c := startServer(t, session)

	raw := c.mustResult("map/outline", serve.Params{Args: []string{"src/a.ts"}, Format: "text"})
	var rendered struct {
		Rendered *string `json:"rendered"`
	}
	if err := json.Unmarshal(raw, &rendered); err != nil || rendered.Rendered == nil {
		t.Fatalf("format:text result = %s, want {rendered: …} (err %v)", raw, err)
	}

	want := localOutput(t, session, "map", "outline", []string{"src/a.ts"}, cli.FormatText)
	if *rendered.Rendered != want {
		t.Errorf("server-rendered text differs from local output\nserver: %q\nlocal:  %q", *rendered.Rendered, want)
	}

	// Bad format values are invalid params.
	c.mustError("map/outline", serve.Params{Args: []string{"src/a.ts"}, Format: "yaml"}, serve.CodeInvalidParams)
}

// startSocketServer runs a Server on a short temp unix socket and shuts it
// down at test cleanup.
func startSocketServer(t *testing.T, session *serve.Session) string {
	t.Helper()
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("unix sockets not available")
	}
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
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	server := serve.NewServer(session, io.Discard)
	go func() {
		defer close(done)
		_ = server.Serve(ctx, ln, path)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return path
}

func TestRouteCommandTextMatchesLocal(t *testing.T) {
	t.Parallel()
	session, _ := newTestSession(t, map[string]any{"/project/src/a.ts": validA}, nil)
	path := startSocketServer(t, session)

	var stdout, stderr bytes.Buffer
	code, err := serve.RouteCommand(path, "map/outline",
		serve.Params{Args: []string{"src/a.ts"}, Format: "text"}, &stdout, &stderr)
	if err != nil || code != cli.ExitOK {
		t.Fatalf("RouteCommand: code=%d err=%v stderr=%q", code, err, stderr.String())
	}
	want := localOutput(t, session, "map", "outline", []string{"src/a.ts"}, cli.FormatText)
	if stdout.String() != want {
		t.Errorf("routed text output differs from local run\nrouted: %q\nlocal:  %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Errorf("unexpected stderr: %q", stderr.String())
	}
}

func TestRouteCommandJSONMatchesLocal(t *testing.T) {
	t.Parallel()
	session, _ := newTestSession(t, map[string]any{"/project/src/a.ts": validA}, nil)
	path := startSocketServer(t, session)

	var stdout, stderr bytes.Buffer
	code, err := serve.RouteCommand(path, "map/outline",
		serve.Params{Args: []string{"src/a.ts"}, Format: "json"}, &stdout, &stderr)
	if err != nil || code != cli.ExitOK {
		t.Fatalf("RouteCommand: code=%d err=%v stderr=%q", code, err, stderr.String())
	}
	want := localOutput(t, session, "map", "outline", []string{"src/a.ts"}, cli.FormatJSON)
	if stdout.String() != want {
		t.Errorf("routed json output differs from local run\nrouted: %q\nlocal:  %q", stdout.String(), want)
	}
}

func TestRouteCommandErrorMapsExitCode(t *testing.T) {
	t.Parallel()
	session, _ := newTestSession(t, map[string]any{"/project/src/a.ts": validA}, nil)
	path := startSocketServer(t, session)

	var stdout, stderr bytes.Buffer
	code, err := serve.RouteCommand(path, "map/outline",
		serve.Params{Args: []string{"src/missing.ts"}, Format: "text"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("RouteCommand transport error: %v", err)
	}
	if code != cli.ExitNotFound {
		t.Errorf("exit code = %d, want %d (not found)", code, cli.ExitNotFound)
	}
	if !strings.Contains(stderr.String(), "tsagent:") {
		t.Errorf("stderr = %q, want error message", stderr.String())
	}
}

func TestRouteCommandDialFailure(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("unix sockets not available")
	}
	dir, err := os.MkdirTemp("", "tsa")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	var stdout, stderr bytes.Buffer
	_, routeErr := serve.RouteCommand(filepath.Join(dir, "nope.sock"), "check", serve.Params{Format: "text"}, &stdout, &stderr)
	if routeErr == nil {
		t.Fatal("RouteCommand against missing socket: want transport error, got nil")
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty on dial failure", stdout.String())
	}
}
