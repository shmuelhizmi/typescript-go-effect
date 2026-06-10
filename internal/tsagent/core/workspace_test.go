package core

import (
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/bundled"
	"github.com/microsoft/typescript-go/internal/vfs/vfstest"
)

func TestWorkspaceProjectDirWalksUpToCoveringTsconfig(t *testing.T) {
	t.Parallel()
	if !bundled.Embedded {
		t.Skip("bundled files are not embedded")
	}
	fs := bundled.WrapFS(vfstest.FromMap(map[string]any{
		"/project/tsconfig.json":               `{"compilerOptions": {"strict": true, "target": "esnext"}}`,
		"/project/packages/lib/src/a.ts":       "export const a = 1;\n",
		"/project/packages/lib/src/nested.txt": "not ts\n",
	}, true /*useCaseSensitiveFileNames*/))
	ws, err := NewWorkspace(Options{
		Project:        "/project/packages/lib",
		Cwd:            "/project",
		FS:             fs,
		SingleThreaded: true,
	})
	if err != nil {
		t.Fatalf("NewWorkspace from a nested --project dir: %v", err)
	}
	if ws.ConfigPath != "/project/tsconfig.json" {
		t.Errorf("ConfigPath = %q, want the ancestor /project/tsconfig.json", ws.ConfigPath)
	}
	if ws.RootDir != "/project" {
		t.Errorf("RootDir = %q, want /project (paths are root-relative after walk-up)", ws.RootDir)
	}
}

func TestWorkspaceProjectDirWithoutAnyTsconfigMentionsWalk(t *testing.T) {
	t.Parallel()
	if !bundled.Embedded {
		t.Skip("bundled files are not embedded")
	}
	fs := bundled.WrapFS(vfstest.FromMap(map[string]any{
		"/repo/packages/lib/src/a.ts": "export const a = 1;\n",
	}, true /*useCaseSensitiveFileNames*/))
	_, err := NewWorkspace(Options{
		Project:        "/repo/packages/lib",
		Cwd:            "/repo",
		FS:             fs,
		SingleThreaded: true,
	})
	if err == nil {
		t.Fatal("expected an error when no tsconfig.json exists anywhere")
	}
	if !strings.Contains(err.Error(), "parent directory") {
		t.Errorf("error should mention the parent-directory walk: %v", err)
	}
}
