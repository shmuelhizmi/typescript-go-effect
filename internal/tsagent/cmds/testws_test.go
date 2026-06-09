package cmds

import (
	"testing"

	"github.com/microsoft/typescript-go/internal/bundled"
	"github.com/microsoft/typescript-go/internal/tsagent/core"
	"github.com/microsoft/typescript-go/internal/vfs/vfstest"
)

// newTestWorkspace mirrors the core test helper: an in-memory case-sensitive
// FS rooted at /project with a default strict tsconfig.
func newTestWorkspace(t *testing.T, files map[string]any) *core.Workspace {
	t.Helper()
	if !bundled.Embedded {
		t.Skip("bundled files are not embedded")
	}
	if _, ok := files["/project/tsconfig.json"]; !ok {
		files["/project/tsconfig.json"] = `{"compilerOptions": {"strict": true, "target": "esnext"}}`
	}
	fs := bundled.WrapFS(vfstest.FromMap(files, true /*useCaseSensitiveFileNames*/))
	ws, err := core.NewWorkspace(core.Options{
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
