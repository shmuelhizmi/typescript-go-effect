package parser_test

import (
	"testing"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/parser"
	"github.com/microsoft/typescript-go/internal/tspath"
	"gotest.tools/v3/assert"
)

func parseETS(t *testing.T, sourceText string) *ast.SourceFile {
	t.Helper()
	fileName := "/test.ets"
	opts := ast.SourceFileParseOptions{
		FileName: fileName,
		Path:     tspath.Path(fileName),
	}
	return parser.ParseSourceFile(opts, sourceText, core.ScriptKindETS)
}

func TestEffectScriptDeclarationLowering(t *testing.T) {
	t.Parallel()
	file := parseETS(t, `
effect getNum(n: number) {
  x <- eb
  <- eb
  if (n < 0) raise new Error("neg")
  const sum = (<- eb) + x + n
  return sum
}
`)
	assert.Equal(t, len(file.Diagnostics()), 0, "expected no parse diagnostics")

	// First statement is the injected import.
	stmts := file.Statements.Nodes
	assert.Assert(t, len(stmts) == 2)
	assert.Equal(t, stmts[0].Kind, ast.KindImportDeclaration)

	// effect declaration lowered to: const getNum = Effect.fn("getNum")(function* ...)
	decl := stmts[1]
	assert.Equal(t, decl.Kind, ast.KindVariableStatement)
	declList := decl.AsVariableStatement().DeclarationList.AsVariableDeclarationList()
	assert.Assert(t, declList.Flags&ast.NodeFlagsConst != 0)
	v := declList.Declarations.Nodes[0].AsVariableDeclaration()
	assert.Equal(t, v.Name().Text(), "getNum")
	outer := v.Initializer
	assert.Equal(t, outer.Kind, ast.KindCallExpression)
	gen := outer.AsCallExpression().Arguments.Nodes[0]
	assert.Equal(t, gen.Kind, ast.KindFunctionExpression)
	assert.Assert(t, gen.AsFunctionExpression().AsteriskToken != nil, "must lower to a generator")

	body := gen.AsFunctionExpression().Body.AsBlock().Statements.Nodes
	// x <- eb  ==>  const x = yield* eb
	bind := body[0]
	assert.Equal(t, bind.Kind, ast.KindVariableStatement)
	bindInit := bind.AsVariableStatement().DeclarationList.AsVariableDeclarationList().Declarations.Nodes[0].AsVariableDeclaration().Initializer
	assert.Equal(t, bindInit.Kind, ast.KindYieldExpression)
	assert.Assert(t, bindInit.AsYieldExpression().AsteriskToken != nil)
	// <- eb  ==>  yield* eb;
	discard := body[1]
	assert.Equal(t, discard.Kind, ast.KindExpressionStatement)
	assert.Equal(t, discard.AsExpressionStatement().Expression.Kind, ast.KindYieldExpression)
}

func TestEffectScriptBlockExpression(t *testing.T) {
	t.Parallel()
	file := parseETS(t, `
const block = effect {
  v <- run()
  return v + 1
};
`)
	assert.Equal(t, len(file.Diagnostics()), 0)
	stmts := file.Statements.Nodes
	assert.Equal(t, stmts[0].Kind, ast.KindImportDeclaration)
	init := stmts[1].AsVariableStatement().DeclarationList.AsVariableDeclarationList().Declarations.Nodes[0].AsVariableDeclaration().Initializer
	assert.Equal(t, init.Kind, ast.KindCallExpression) // Effect.gen(...)
}

func TestEffectScriptPlainTSUnaffected(t *testing.T) {
	t.Parallel()
	// Valid TS must stay valid with identical meaning outside effect bodies.
	file := parseETS(t, `
const effect = 1;
const raise = (n: number) => n;
raise(effect);
function f(a: number, b: number) {
  return a < -b; // relational, not a bind
}
`)
	assert.Equal(t, len(file.Diagnostics()), 0)
	for _, s := range file.Statements.Nodes {
		assert.Assert(t, s.Kind != ast.KindImportDeclaration, "no helper import for plain TS")
	}
}

func TestEffectScriptNotEnabledInTS(t *testing.T) {
	t.Parallel()
	fileName := "/test.ts"
	opts := ast.SourceFileParseOptions{FileName: fileName, Path: tspath.Path(fileName)}
	file := parser.ParseSourceFile(opts, `
effect getNum(n: number) {
  x <- eb
}
`, core.ScriptKindTS)
	// In a .ts file this is not EffectScript; it must produce parse errors
	// rather than silently lowering.
	assert.Assert(t, len(file.Diagnostics()) > 0)
}

func TestEffectScriptServiceDeclaration(t *testing.T) {
	t.Parallel()
	file := parseETS(t, `
export service Database {
  query(sql: string): number
}
`)
	assert.Equal(t, len(file.Diagnostics()), 0)
	stmts := file.Statements.Nodes
	// import + class
	assert.Equal(t, stmts[0].Kind, ast.KindImportDeclaration)
	cls := stmts[1]
	assert.Equal(t, cls.Kind, ast.KindClassDeclaration)
	assert.Equal(t, cls.Name().Text(), "Database")
	heritage := cls.AsClassDeclaration().HeritageClauses.Nodes[0]
	assert.Equal(t, heritage.Kind, ast.KindHeritageClause)
}

func TestEffectScriptLayerDeclarations(t *testing.T) {
	t.Parallel()
	file := parseETS(t, `
layer A: Tag { return 1 }
scoped layer B: Tag { return 2 }
layer C: Tag = value
layer D: Tag provide [A] { return 3 }
`)
	assert.Equal(t, len(file.Diagnostics()), 0)
	stmts := file.Statements.Nodes
	assert.Equal(t, stmts[0].Kind, ast.KindImportDeclaration)
	for _, s := range stmts[1:] {
		assert.Equal(t, s.Kind, ast.KindVariableStatement)
	}
}

func TestEffectScriptConcurrencyAndDefer(t *testing.T) {
	t.Parallel()
	file := parseETS(t, `
effect work() {
  pair <- par [ea, eb]
  named <- par { left: ea }
  bounded <- par(4) [ea]
  winner <- race [ea, eb]
  third <- race [ea, eb, ec]
  fiber <- fork ea
  defer { <- ea }
  defer (exit) { <- ea }
  joined <- join fiber
  return [pair, named, bounded, winner, third, joined]
}
`)
	assert.Equal(t, len(file.Diagnostics()), 0)
}

func TestEffectScriptPipeline(t *testing.T) {
	t.Parallel()
	file := parseETS(t, `
const a = 1 |> double;
const b = 1 |> add(2) |> double;
const c = x | y; // plain bitwise-or unaffected
`)
	assert.Equal(t, len(file.Diagnostics()), 0)
	stmts := file.Statements.Nodes
	// pipelines lower to calls, no helper import is required
	assert.Assert(t, stmts[0].Kind != ast.KindImportDeclaration)
	aInit := stmts[0].AsVariableStatement().DeclarationList.AsVariableDeclarationList().Declarations.Nodes[0].AsVariableDeclaration().Initializer
	assert.Equal(t, aInit.Kind, ast.KindCallExpression)
	assert.Equal(t, aInit.AsCallExpression().Expression.Text(), "double")
	cInit := stmts[2].AsVariableStatement().DeclarationList.AsVariableDeclarationList().Declarations.Nodes[0].AsVariableDeclaration().Initializer
	assert.Equal(t, cInit.Kind, ast.KindBinaryExpression)
}

func TestEffectScriptDestructuringBinds(t *testing.T) {
	t.Parallel()
	file := parseETS(t, `
effect work() {
  [a, b] <- ea
  { x, y } <- eb
  return a + b + x + y
}
`)
	assert.Equal(t, len(file.Diagnostics()), 0)
}

func TestEffectScriptPostfixCatch(t *testing.T) {
	t.Parallel()
	file := parseETS(t, `
effect catches(id: string) {
  html <- eb catch {
    NotFound as e            >> 0
    DbError | NetError as e  >> raise new Error("503")
    _ as e                   >> { <- eb; return 1 }
  }
  return html
}
`)
	assert.Equal(t, len(file.Diagnostics()), 0)
}

func TestEffectScriptRaiseExpression(t *testing.T) {
	t.Parallel()
	file := parseETS(t, `
effect f(id: string) {
  const v = id !== "" ? id : raise new Error("empty")
  return v
}
`)
	assert.Equal(t, len(file.Diagnostics()), 0)
}
