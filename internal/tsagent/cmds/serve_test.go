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
