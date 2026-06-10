package astnav_test

import (
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/astnav"
	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/parser"
)

const effectScriptNavSource = `service Greeter {
  greeting: Effect.Effect<string>
}

layer GreeterLive: Greeter {
  return { greeting: Effect.succeed("Hello") }
}

effect findUser(id: string): string raises NotFound {
  if (id === "") raise new NotFound({ id })
  return ` + "`user-${id}`" + `
}

effect main(): string {
  user <- findUser("42") catch {
    NotFound as e >> ` + "`fallback-${e.id}`" + `
  }
  return user
}
`

func parseEffectScriptNavFile(t *testing.T) *ast.SourceFile {
	t.Helper()
	return parser.ParseSourceFile(ast.SourceFileParseOptions{
		FileName: "/main.ets",
		Path:     "/main.ets",
	}, effectScriptNavSource, core.ScriptKindETS)
}

func navPosition(t *testing.T, needle string, word string) int {
	t.Helper()
	idx := strings.Index(effectScriptNavSource, needle)
	if idx < 0 {
		t.Fatalf("needle %q not found", needle)
	}
	col := strings.Index(effectScriptNavSource[idx:], word)
	if col < 0 {
		t.Fatalf("word %q not found in needle %q", word, needle)
	}
	return idx + col + 1
}

// EffectScript keywords lower away at parse time, so their source spans are
// gaps in the AST. GetTouchingPropertyName must synthesize a token for them
// (not panic on the lowering's positionless node lists) and flag it Reparsed
// so consumers know it has no symbol behind it.
func TestEffectScriptKeywordGapTokens(t *testing.T) {
	t.Parallel()
	file := parseEffectScriptNavFile(t)
	for _, probe := range []struct{ needle, word string }{
		{"service Greeter", "service"},
		{"layer GreeterLive", "layer"},
		{"effect findUser", "effect"},
		{"raises NotFound", "raises"},
		{`if (id === "") raise`, "raise"},
	} {
		pos := navPosition(t, probe.needle, probe.word)
		node := astnav.GetTouchingPropertyName(file, pos)
		if node.Kind != ast.KindIdentifier {
			t.Errorf("%s: expected gap identifier token, got %v", probe.word, node.Kind)
		}
		if node.Flags&ast.NodeFlagsReparsed == 0 {
			t.Errorf("%s: gap token should be flagged Reparsed", probe.word)
		}
	}
}

// User-written identifiers inside lowered constructs keep real positions and
// must be reachable: the catch-arm binding becomes a parameter, a service
// member stays a property signature.
func TestEffectScriptRealNodesReachable(t *testing.T) {
	t.Parallel()
	file := parseEffectScriptNavFile(t)
	for _, probe := range []struct {
		needle, word string
		parentKind   ast.Kind
	}{
		{"NotFound as e >>", "e", ast.KindParameter},
		{"greeting: Effect.Effect<string>", "greeting", ast.KindPropertySignature},
		{"user <- findUser", "user", ast.KindVariableDeclaration},
	} {
		pos := navPosition(t, probe.needle, probe.word)
		node := astnav.GetTouchingPropertyName(file, pos)
		if node.Kind != ast.KindIdentifier || node.Flags&ast.NodeFlagsReparsed != 0 {
			t.Errorf("%s: expected a real identifier, got %v (flags %v)", probe.word, node.Kind, node.Flags)
			continue
		}
		if node.Parent == nil || node.Parent.Kind != probe.parentKind {
			t.Errorf("%s: expected parent %v, got %v", probe.word, probe.parentKind, node.Parent.Kind)
		}
	}
}

// All nodes synthesized by the EffectScript lowering must either carry real
// source positions or the Reparsed flag; a positionless unflagged node breaks
// position-driven machinery (token search runs the scanner from -1).
func TestEffectScriptLoweredPositionsFlagged(t *testing.T) {
	t.Parallel()
	file := parseEffectScriptNavFile(t)
	var walk func(node *ast.Node)
	walk = func(node *ast.Node) {
		if node == nil {
			return
		}
		if (node.Pos() < 0 || node.End() < 0) && node.Flags&ast.NodeFlagsReparsed == 0 {
			t.Errorf("node %v has synthesized positions (%d, %d) but no Reparsed flag", node.Kind, node.Pos(), node.End())
		}
		node.ForEachChild(func(child *ast.Node) bool {
			walk(child)
			return false
		})
	}
	walk(file.AsNode())
}
