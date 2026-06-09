package core

import (
	"testing"

	"github.com/microsoft/typescript-go/internal/bundled"
	"github.com/microsoft/typescript-go/internal/vfs/vfstest"
)

// newTestWorkspace builds a Workspace over an in-memory case-sensitive FS.
// Paths must be normalized absolute POSIX paths (e.g. /project/src/index.ts).
// A /project/tsconfig.json is added when not provided.
func newTestWorkspace(t *testing.T, files map[string]any) *Workspace {
	t.Helper()
	if !bundled.Embedded {
		t.Skip("bundled files are not embedded")
	}
	if _, ok := files["/project/tsconfig.json"]; !ok {
		files["/project/tsconfig.json"] = `{"compilerOptions": {"strict": true, "target": "esnext"}}`
	}
	fs := bundled.WrapFS(vfstest.FromMap(files, true /*useCaseSensitiveFileNames*/))
	ws, err := NewWorkspace(Options{
		Project:        "/project",
		Cwd:            "/project",
		FS:             fs,
		SingleThreaded: true,
	})
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	return ws
}
