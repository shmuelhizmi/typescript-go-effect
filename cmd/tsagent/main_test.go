package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/bundled"
	"github.com/microsoft/typescript-go/internal/tsagent/serve"
	"github.com/microsoft/typescript-go/internal/vfs/vfstest"
)

// TestGlobalSocketPathRouting: the global --socket-path flag lets one-shot
// commands reach a daemon started with a custom --socket-path (the derived
// default would never match). The daemon owns an in-memory project, so a
// successful routed run proves the socket override was used end to end.
func TestGlobalSocketPathRouting(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("unix sockets not available")
	}
	if !bundled.Embedded {
		t.Skip("bundled files are not embedded")
	}

	fs := bundled.WrapFS(vfstest.FromMap(map[string]any{
		"/project/tsconfig.json": `{"compilerOptions": {"strict": true, "target": "esnext"}}`,
		"/project/src/a.ts":      "export const answer: number = 42;\n",
	}, true /*useCaseSensitiveFileNames*/))
	session, err := serve.NewSession(serve.SessionOptions{
		Project:        "/project",
		Cwd:            "/project",
		FS:             fs,
		SingleThreaded: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	server := serve.NewServer(session, io.Discard)

	// Keep the path under the unix sockaddr limit (~104 bytes on darwin).
	dir, err := os.MkdirTemp("", "tsa")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "t.sock")
	ln, err := serve.Listen(socketPath)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, ln, socketPath) }()

	var stdout, stderr bytes.Buffer
	code := run([]string{"map", "outline", "src/a.ts", "--connect", "require", "--socket-path", socketPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("routed run exit code = %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "answer") {
		t.Errorf("routed outline output missing the daemon project's symbol:\n%s", stdout.String())
	}

	cancel()
	<-done
}
