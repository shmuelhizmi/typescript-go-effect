package cmds

import (
	"context"
	"testing"
)

const navHierarchySource = `export interface Animal {
	name: string;
	speak(): string;
}

export interface Pet extends Animal {
	owner: string;
}

export class Dog implements Pet {
	name = "";
	owner = "";
	speak(): string {
		return "woof";
	}
}

export class Puppy extends Dog {}

// Satisfies Pet structurally but has no implements clause.
export class RobotDog {
	name = "";
	owner = "";
	speak(): string {
		return "beep";
	}
}

export class AppError extends Error {
	code = 1;
}
`

func navTypesProjectFiles() map[string]any {
	return map[string]any{
		"/project/src/hierarchy.ts": navHierarchySource,
	}
}

func hierarchyByName(entries []*TypeHierarchyEntry) map[string]*TypeHierarchyEntry {
	byName := make(map[string]*TypeHierarchyEntry)
	for _, e := range entries {
		byName[e.Name] = e
	}
	return byName
}

func TestNavTypesUp(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, navTypesProjectFiles())
	result, err := runNavTypes(context.Background(), ws, &typesFlags{name: "Puppy", direction: "up"}, nil)
	if err != nil {
		t.Fatalf("runNavTypes: %v", err)
	}
	if result.Name != "Puppy" || result.File != "src/hierarchy.ts" {
		t.Errorf("target = %s %s, want Puppy src/hierarchy.ts", result.Name, result.File)
	}
	if result.SymbolID != "src/hierarchy.ts#Puppy" {
		t.Errorf("target symbolId = %q", result.SymbolID)
	}
	if len(result.Up) != 3 {
		t.Fatalf("up = %+v, want Dog, Pet, Animal", result.Up)
	}
	wantChain := []struct {
		name     string
		relation string
		depth    int
	}{
		{"Dog", "extends", 1},
		{"Pet", "implements", 2},
		{"Animal", "extends", 3},
	}
	for i, want := range wantChain {
		got := result.Up[i]
		if got.Name != want.name || got.Relation != want.relation || got.Depth != want.depth {
			t.Errorf("up[%d] = %s/%s/%d, want %s/%s/%d", i, got.Name, got.Relation, got.Depth, want.name, want.relation, want.depth)
		}
		if got.File != "src/hierarchy.ts" || got.SymbolID == "" || got.External {
			t.Errorf("up[%d] location = %+v, want project-local with symbol id", i, got)
		}
	}
	if len(result.Down) != 0 {
		t.Errorf("direction=up should not fill down, got %+v", result.Down)
	}
}

func TestNavTypesUpLibParentExternal(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, navTypesProjectFiles())
	result, err := runNavTypes(context.Background(), ws, &typesFlags{name: "AppError", direction: "up"}, nil)
	if err != nil {
		t.Fatalf("runNavTypes: %v", err)
	}
	if len(result.Up) == 0 {
		t.Fatal("expected Error parent")
	}
	errorEntry := hierarchyByName(result.Up)["Error"]
	if errorEntry == nil {
		t.Fatalf("up = %+v, want Error", result.Up)
	}
	if !errorEntry.External {
		t.Error("lib parent Error should be marked external")
	}
	if errorEntry.File != "" || errorEntry.SymbolID != "" {
		t.Errorf("external entry should not carry project file/symbolId, got %+v", errorEntry)
	}
}

func TestNavTypesDown(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, navTypesProjectFiles())
	result, err := runNavTypes(context.Background(), ws, &typesFlags{name: "Animal", direction: "down"}, nil)
	if err != nil {
		t.Fatalf("runNavTypes: %v", err)
	}
	if len(result.Down) != 3 {
		t.Fatalf("down = %+v, want Pet, Dog, Puppy", result.Down)
	}
	byName := hierarchyByName(result.Down)
	for name, want := range map[string]struct {
		relation string
		depth    int
	}{
		"Pet":   {"extends", 1},
		"Dog":   {"implements", 2},
		"Puppy": {"extends", 3},
	} {
		got := byName[name]
		if got == nil {
			t.Errorf("missing %s in down", name)
			continue
		}
		if got.Relation != want.relation || got.Depth != want.depth || got.Structural {
			t.Errorf("%s = %s/%d structural=%v, want %s/%d", name, got.Relation, got.Depth, got.Structural, want.relation, want.depth)
		}
		if got.SymbolID != "src/hierarchy.ts#"+name {
			t.Errorf("%s symbolId = %q", name, got.SymbolID)
		}
	}
	if byName["RobotDog"] != nil {
		t.Error("RobotDog must not appear without --structural")
	}
}

func TestNavTypesStructural(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, navTypesProjectFiles())
	result, err := runNavTypes(context.Background(), ws, &typesFlags{name: "Pet", direction: "down", structural: true}, nil)
	if err != nil {
		t.Fatalf("runNavTypes: %v", err)
	}
	byName := hierarchyByName(result.Down)
	if dog := byName["Dog"]; dog == nil || dog.Relation != "implements" || dog.Structural {
		t.Errorf("Dog should be a declared implementer, got %+v", dog)
	}
	robot := byName["RobotDog"]
	if robot == nil {
		t.Fatalf("down = %+v, want structural RobotDog", result.Down)
	}
	if robot.Relation != "structural" || !robot.Structural || robot.Depth != 1 {
		t.Errorf("RobotDog = %+v, want structural depth 1", robot)
	}
	// Animal does not satisfy Pet (missing owner) and AppError is unrelated.
	if byName["Animal"] != nil || byName["AppError"] != nil {
		t.Errorf("unexpected structural matches: %+v", result.Down)
	}
}

func TestNavTypesDepthLimit(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, navTypesProjectFiles())
	ctx := context.Background()

	result, err := runNavTypes(ctx, ws, &typesFlags{name: "Animal", direction: "down", depth: 1}, nil)
	if err != nil {
		t.Fatalf("runNavTypes: %v", err)
	}
	if len(result.Down) != 1 || result.Down[0].Name != "Pet" {
		t.Errorf("down depth=1 = %+v, want only Pet", result.Down)
	}

	result, err = runNavTypes(ctx, ws, &typesFlags{name: "Puppy", direction: "up", depth: 1}, nil)
	if err != nil {
		t.Fatalf("runNavTypes: %v", err)
	}
	if len(result.Up) != 1 || result.Up[0].Name != "Dog" {
		t.Errorf("up depth=1 = %+v, want only Dog", result.Up)
	}
}

func TestNavTypesInvalidTarget(t *testing.T) {
	t.Parallel()
	ws := newTestWorkspace(t, navTypesProjectFiles())
	if _, err := runNavTypes(context.Background(), ws, &typesFlags{name: "Animal", direction: "sideways"}, nil); err == nil {
		t.Error("expected error for invalid --direction")
	}
	files := map[string]any{"/project/src/x.ts": "export const notAClass = 1;\n"}
	ws2 := newTestWorkspace(t, files)
	if _, err := runNavTypes(context.Background(), ws2, &typesFlags{name: "notAClass", direction: "both"}, nil); err == nil {
		t.Error("expected error for non-class target")
	}
}
