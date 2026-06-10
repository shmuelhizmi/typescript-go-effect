package cmds

import (
	"context"
	"testing"
)

func navExportGraphProjectFiles() map[string]any {
	return map[string]any{
		"/project/src/lib/a.ts": `export class A {}

export function helper(): void {}
`,
		// Hop 1: barrel with a rename.
		"/project/src/lib/index.ts": `export { A as B } from "./a";
export const other = 1;
`,
		// Hop 2: star re-export from the package entry point.
		"/project/src/index.ts": `export * from "./lib/index";
`,
		// import-then-export style re-export with a rename.
		"/project/src/facade.ts": `import { helper } from "./lib/a";

export { helper as help };
`,
	}
}

func TestNavExportGraphBarrelChain(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, navExportGraphProjectFiles())
	result, err := runNavExportGraph(context.Background(), ws, &exportGraphFlags{name: "A"}, nil)
	if err != nil {
		t.Fatalf("runNavExportGraph: %v", err)
	}
	root := result.Root
	if root.File != "src/lib/a.ts" || root.ExportedAs != "A" || root.Via != "declaration" || root.Line != 1 {
		t.Fatalf("root = %+v, want declaration of A at src/lib/a.ts:1", root)
	}
	if result.SymbolID != "src/lib/a.ts#A" {
		t.Errorf("symbolId = %q", result.SymbolID)
	}
	if len(root.Hops) != 1 {
		t.Fatalf("root hops = %+v, want one barrel hop", root.Hops)
	}
	barrel := root.Hops[0]
	if barrel.File != "src/lib/index.ts" || barrel.ExportedAs != "B" || barrel.Via != "named" {
		t.Errorf("hop 1 = %+v, want B via named in src/lib/index.ts", barrel)
	}
	if barrel.PublicEntry {
		t.Error("non-leaf barrel must not be marked public-entry")
	}
	if len(barrel.Hops) != 1 {
		t.Fatalf("barrel hops = %+v, want one star hop", barrel.Hops)
	}
	star := barrel.Hops[0]
	if star.File != "src/index.ts" || star.ExportedAs != "B" || star.Via != "star" {
		t.Errorf("hop 2 = %+v, want B via star in src/index.ts", star)
	}
	if !star.PublicEntry {
		t.Error("leaf index.* file should be marked public-entry")
	}
	if len(star.Hops) != 0 {
		t.Errorf("leaf should have no hops, got %+v", star.Hops)
	}
}

func TestNavExportGraphImportThenExport(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, navExportGraphProjectFiles())
	result, err := runNavExportGraph(context.Background(), ws, &exportGraphFlags{name: "helper"}, nil)
	if err != nil {
		t.Fatalf("runNavExportGraph: %v", err)
	}
	root := result.Root
	if root.File != "src/lib/a.ts" || root.ExportedAs != "helper" {
		t.Fatalf("root = %+v", root)
	}
	if len(root.Hops) != 1 {
		t.Fatalf("hops = %+v, want only the facade re-export", root.Hops)
	}
	facade := root.Hops[0]
	if facade.File != "src/facade.ts" || facade.ExportedAs != "help" || facade.Via != "named" {
		t.Errorf("facade hop = %+v, want help via named", facade)
	}
	if facade.PublicEntry {
		t.Error("facade.ts is not an index.* entry point")
	}
}

func TestNavExportGraphLocalAliasAndUnexported(t *testing.T) {
	t.Parallel()
	files := map[string]any{
		// Local rename in the declaring module itself, then a named hop.
		"/project/src/impl.ts": `class Hidden {}

export { Hidden as Visible };
`,
		"/project/src/index.ts": `export { Visible } from "./impl";
`,
	}
	ws := newTestWorkspace(t, files)
	result, err := runNavExportGraph(context.Background(), ws, &exportGraphFlags{name: "Hidden"}, nil)
	if err != nil {
		t.Fatalf("runNavExportGraph: %v", err)
	}
	root := result.Root
	if root.ExportedAs != "Hidden" || len(root.Hops) != 1 {
		t.Fatalf("root = %+v, want one local-alias hop", root)
	}
	alias := root.Hops[0]
	if alias.File != "src/impl.ts" || alias.ExportedAs != "Visible" || alias.Via != "named" {
		t.Fatalf("alias hop = %+v, want Visible via named in src/impl.ts", alias)
	}
	if len(alias.Hops) != 1 || alias.Hops[0].File != "src/index.ts" || !alias.Hops[0].PublicEntry {
		t.Errorf("index hop = %+v, want public-entry src/index.ts", alias.Hops)
	}
}
