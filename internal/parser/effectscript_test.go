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

func TestEffectScriptInlineLayerExpression(t *testing.T) {
	t.Parallel()
	file := parseETS(t, `
const live = layer Greeter {
  return { greeting: "hi" }
}
const cfg = layer Config ({ url: "x" })
main() |> Effect.provide(layer Greeter { return {} })
`)
	assert.Equal(t, len(file.Diagnostics()), 0)
	stmts := file.Statements.Nodes
	assert.Equal(t, stmts[0].Kind, ast.KindImportDeclaration)

	// const live = Layer.effect(Greeter, Effect.gen(function* () { ... }))
	live := stmts[1].AsVariableStatement().DeclarationList.AsVariableDeclarationList().Declarations.Nodes[0].AsVariableDeclaration().Initializer
	assert.Equal(t, live.Kind, ast.KindCallExpression)
	effectAccess := live.AsCallExpression().Expression.AsPropertyAccessExpression()
	assert.Equal(t, effectAccess.Expression.Text(), "Layer")
	assert.Equal(t, effectAccess.Name().Text(), "effect")
	liveArgs := live.AsCallExpression().Arguments.Nodes
	assert.Equal(t, liveArgs[0].Text(), "Greeter")
	assert.Equal(t, liveArgs[1].Kind, ast.KindCallExpression) // Effect.gen(...)

	// const cfg = Layer.succeed(Config, { url: "x" })
	cfg := stmts[2].AsVariableStatement().DeclarationList.AsVariableDeclarationList().Declarations.Nodes[0].AsVariableDeclaration().Initializer
	assert.Equal(t, cfg.Kind, ast.KindCallExpression)
	cfgAccess := cfg.AsCallExpression().Expression.AsPropertyAccessExpression()
	assert.Equal(t, cfgAccess.Expression.Text(), "Layer")
	assert.Equal(t, cfgAccess.Name().Text(), "succeed")
	cfgArgs := cfg.AsCallExpression().Arguments.Nodes
	assert.Equal(t, cfgArgs[0].Text(), "Config")
	assert.Equal(t, cfgArgs[1].Kind, ast.KindObjectLiteralExpression)
}

func TestEffectScriptLayerStillCallableIdentifier(t *testing.T) {
	t.Parallel()
	// 'layer' as a plain identifier (variable, call) must keep working: only
	// `layer Ident (`/`layer Ident {` is the inline form.
	file := parseETS(t, `
const layer = (tag: unknown) => tag;
const x = layer(1);
const y = layer;
`)
	assert.Equal(t, len(file.Diagnostics()), 0)
	for _, s := range file.Statements.Nodes {
		assert.Assert(t, s.Kind != ast.KindImportDeclaration, "no helper import for plain TS")
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

// effectBodyStatements returns the statements of the generator body that an
// `effect name() { ... }` declaration lowers to (after the injected import).
func effectBodyStatements(t *testing.T, file *ast.SourceFile) []*ast.Node {
	t.Helper()
	stmts := file.Statements.Nodes
	decl := stmts[len(stmts)-1]
	v := decl.AsVariableStatement().DeclarationList.AsVariableDeclarationList().Declarations.Nodes[0].AsVariableDeclaration()
	gen := v.Initializer.AsCallExpression().Arguments.Nodes[0]
	return gen.AsFunctionExpression().Body.AsBlock().Statements.Nodes
}

func bindDeclaration(t *testing.T, stmt *ast.Node) *ast.VariableDeclaration {
	t.Helper()
	return stmt.AsVariableStatement().DeclarationList.AsVariableDeclarationList().Declarations.Nodes[0].AsVariableDeclaration()
}

// The reported ASI bug: an expression statement (`user.length`) must NOT absorb
// the following `[a, b] <- e` as element access — they are two statements.
func TestEffectScriptExprThenDestructuringBind(t *testing.T) {
	t.Parallel()
	file := parseETS(t, `
effect m(user: string) {
  user.length
  [a, b] <- pair()
  return a + b
}
`)
	assert.Equal(t, len(file.Diagnostics()), 0)
	body := effectBodyStatements(t, file)
	assert.Equal(t, len(body), 3) // expr stmt, destructuring bind, return
	assert.Equal(t, body[0].Kind, ast.KindExpressionStatement)
	assert.Equal(t, body[0].AsExpressionStatement().Expression.Kind, ast.KindPropertyAccessExpression)
	assert.Equal(t, body[1].Kind, ast.KindVariableStatement)
	bd := bindDeclaration(t, body[1])
	assert.Equal(t, bd.Name().Kind, ast.KindArrayBindingPattern)
	assert.Equal(t, bd.Initializer.Kind, ast.KindYieldExpression)
	assert.Equal(t, body[2].Kind, ast.KindReturnStatement)
}

// The guard must be precise: with no `<-` following, `arr` <newline> `[0]` stays
// a single element-access expression (not split into two statements).
func TestEffectScriptElementAccessAcrossNewlineStillWorks(t *testing.T) {
	t.Parallel()
	file := parseETS(t, `
effect m(arr: number[]) {
  const first = arr
  [0]
  return first
}
`)
	assert.Equal(t, len(file.Diagnostics()), 0)
	body := effectBodyStatements(t, file)
	assert.Equal(t, len(body), 2) // const first = arr[0];  return first;
	first := bindDeclaration(t, body[0])
	assert.Equal(t, first.Initializer.Kind, ast.KindElementAccessExpression)
}

// Sibling forms whose leading token cannot continue the prior expression are
// already separated correctly; pin them so they never regress.
func TestEffectScriptExprThenDiscardObjectIdentifierBinds(t *testing.T) {
	t.Parallel()
	file := parseETS(t, `
effect m(user: string) {
  user.length
  <- log(user)
  user.length
  { x } <- obj()
  user.length
  y <- val()
  return x + y
}
`)
	assert.Equal(t, len(file.Diagnostics()), 0)
	body := effectBodyStatements(t, file)
	assert.Equal(t, len(body), 7) // expr, discard, expr, objbind, expr, idbind, return

	// discard bind: yield* log(user)
	assert.Equal(t, body[1].Kind, ast.KindExpressionStatement)
	assert.Equal(t, body[1].AsExpressionStatement().Expression.Kind, ast.KindYieldExpression)
	// object destructuring bind
	assert.Equal(t, bindDeclaration(t, body[3]).Name().Kind, ast.KindObjectBindingPattern)
	// identifier bind
	assert.Equal(t, bindDeclaration(t, body[5]).Name().Kind, ast.KindIdentifier)
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

func TestEffectScriptMatchValueMode(t *testing.T) {
	t.Parallel()
	file := parseETS(t, `
const label = match (res) {
  { status: 200, body }      >> body
  { status: 301 | 302 }      >> "redirect"
  { status: s } if s >= 500  >> "server error"
  _                          >> "unknown"
};
`)
	assert.Equal(t, len(file.Diagnostics()), 0)
}

func TestEffectScriptMatchTagMode(t *testing.T) {
	t.Parallel()
	file := parseETS(t, `
const msg = match tag (err) {
  NotFound as e       >> e.id
  DbError | NetError  >> "infra"
  _                   >> "other"
};
`)
	assert.Equal(t, len(file.Diagnostics()), 0)
	stmts := file.Statements.Nodes
	assert.Equal(t, stmts[0].Kind, ast.KindImportDeclaration) // Match import
}

func TestEffectScriptMatchAsCall(t *testing.T) {
	t.Parallel()
	// `match(x)` not followed by `{` stays an ordinary call expression.
	file := parseETS(t, `
declare function match(x: number): number;
const y = match(1) + 2;
`)
	assert.Equal(t, len(file.Diagnostics()), 0)
}

func TestEffectScriptMatchEffectful(t *testing.T) {
	t.Parallel()
	file := parseETS(t, `
effect handle(err: unknown) {
  out <- effect {
    return match tag (err) {
      NotFound as e >> { v <- recover(e); return v }
      _             >> "n/a"
    }
  }
  return out
}
`)
	assert.Equal(t, len(file.Diagnostics()), 0)
}

func TestEffectScriptTypeSugar(t *testing.T) {
	t.Parallel()
	file := parseETS(t, `
type Full = number raises Boom requires Db;
type NoReq = string raises Boom;
type NoErr = string requires Db;
effect annotated(n: number): number raises Boom requires Db {
  return n
}
`)
	assert.Equal(t, len(file.Diagnostics()), 0)
	alias := file.Statements.Nodes[1].AsTypeAliasDeclaration() // after injected import
	ref := alias.Type
	assert.Equal(t, ref.Kind, ast.KindTypeReference)
	assert.Equal(t, len(ref.AsTypeReferenceNode().TypeArguments.Nodes), 3)
}

func TestEffectScriptUsingBindAndAnonEffect(t *testing.T) {
	t.Parallel()
	file := parseETS(t, `
const anon = effect (n: number) {
  return n * 2
};
effect resources(cfg: string) {
  using conn <- acquire(cfg) release (c, exit) { <- c.close() }
  using plain <- acquire(cfg)
  return conn
}
`)
	assert.Equal(t, len(file.Diagnostics()), 0)
}

func TestEffectScriptClassMethods(t *testing.T) {
	t.Parallel()
	file := parseETS(t, `
class UserRepo {
  effect getById(id: string): string {
    v <- load(id)
    return v
  }
  static effect ping(): string {
    return "pong"
  }
  regular() { return 1 }
}
`)
	assert.Equal(t, len(file.Diagnostics()), 0)
	cls := file.Statements.Nodes[1].AsClassDeclaration() // after injected import
	members := cls.Members.Nodes
	assert.Equal(t, members[0].Kind, ast.KindPropertyDeclaration)
	assert.Equal(t, members[1].Kind, ast.KindPropertyDeclaration)
	assert.Equal(t, members[2].Kind, ast.KindMethodDeclaration)
	init := members[0].AsPropertyDeclaration().Initializer
	assert.Equal(t, init.Kind, ast.KindCallExpression)
}

func TestEffectScriptDiagnostics(t *testing.T) {
	t.Parallel()
	codes := func(file *ast.SourceFile) []int32 {
		var out []int32
		for _, d := range file.Diagnostics() {
			out = append(out, d.Code())
		}
		return out
	}

	bindOutside := parseETS(t, "function f(ea: any) {\n  x <- ea\n}\n")
	assert.DeepEqual(t, codes(bindOutside), []int32{18100})

	discardOutside := parseETS(t, "function f(ea: any) {\n  <- ea\n}\n")
	assert.DeepEqual(t, codes(discardOutside), []int32{18100})

	raiseOutside := parseETS(t, "function f() {\n  raise new Error(\"x\")\n}\n")
	assert.DeepEqual(t, codes(raiseOutside), []int32{18101})

	unreachableArm := parseETS(t, `
const m = match (v) {
  _ >> 1
  2 >> 2
};
`)
	assert.DeepEqual(t, codes(unreachableArm), []int32{18151})

	badTagPattern := parseETS(t, `
const m = match tag (v) {
  notATag >> 1
};
`)
	assert.DeepEqual(t, codes(badTagPattern), []int32{18150})

	unreachableCatch := parseETS(t, `
effect f(ea: any) {
  x <- ea catch {
    _ as e >> 1
    NotFound >> 2
  }
  return x
}
`)
	assert.DeepEqual(t, codes(unreachableCatch), []int32{18151})
}

func TestEffectScriptReleaseOnNextLine(t *testing.T) {
	t.Parallel()
	file := parseETS(t, `
effect f() {
  using res <- acquire()
    release (r) { <- r.close() }
  return res
}
`)
	assert.Equal(t, len(file.Diagnostics()), 0)
}

func TestEffectScriptGuardedBindingArm(t *testing.T) {
	t.Parallel()
	file := parseETS(t, `
const sized = match (n) {
  m if m >= 10 >> m * 2
  _            >> 0
};
`)
	assert.Equal(t, len(file.Diagnostics()), 0, "guarded binding arm is not a catch-all")
}

func TestEffectScriptDecoratorsRejected(t *testing.T) {
	t.Parallel()
	file := parseETS(t, `
@withSpan("f")
@retry(schedule)
export effect f() {
  return 1
}
`)
	var diagCodes []int32
	for _, d := range file.Diagnostics() {
		diagCodes = append(diagCodes, d.Code())
	}
	assert.DeepEqual(t, diagCodes, []int32{1206, 1206})

	// Still lowers to `export const f = Effect.fn("f")(generator)` with the
	// decorators dropped and no combinator arguments appended.
	stmts := file.Statements.Nodes
	decl := stmts[len(stmts)-1]
	assert.Equal(t, decl.Kind, ast.KindVariableStatement)
	assert.Assert(t, ast.HasSyntacticModifier(decl, ast.ModifierFlagsExport), "export must survive")
	outer := decl.AsVariableStatement().DeclarationList.AsVariableDeclarationList().Declarations.Nodes[0].AsVariableDeclaration().Initializer
	assert.Equal(t, outer.Kind, ast.KindCallExpression)
	assert.Equal(t, len(outer.AsCallExpression().Arguments.Nodes), 1, "generator only, no combinators")
}

func TestEffectScriptTaggedErrorDeclaration(t *testing.T) {
	t.Parallel()
	file := parseETS(t, `
tagged error NotFound {
  id: string
}

export tagged error Empty {}
`)
	assert.Equal(t, len(file.Diagnostics()), 0, "expected no parse diagnostics")

	stmts := file.Statements.Nodes
	// import (Data injected), NotFound, Empty
	assert.Equal(t, len(stmts), 3)
	assert.Equal(t, stmts[0].Kind, ast.KindImportDeclaration)

	decl := stmts[1].AsClassDeclaration()
	assert.Equal(t, decl.Name().Text(), "NotFound")
	heritage := decl.HeritageClauses.Nodes[0].AsHeritageClause()
	withTypeArgs := heritage.Types.Nodes[0].AsExpressionWithTypeArguments()
	call := withTypeArgs.Expression.AsCallExpression()
	access := call.Expression.AsPropertyAccessExpression()
	assert.Equal(t, access.Expression.Text(), "Data")
	assert.Equal(t, access.Name().Text(), "TaggedError")
	assert.Equal(t, call.Arguments.Nodes[0].Text(), "NotFound")
	assert.Equal(t, len(withTypeArgs.TypeArguments.Nodes), 1)
	shape := withTypeArgs.TypeArguments.Nodes[0]
	assert.Equal(t, shape.Kind, ast.KindTypeLiteral)
	assert.Equal(t, shape.AsTypeLiteralNode().Members.Nodes[0].Name().Text(), "id")

	exported := stmts[2]
	assert.Assert(t, ast.HasSyntacticModifier(exported, ast.ModifierFlagsExport))
	emptyShape := exported.AsClassDeclaration().HeritageClauses.Nodes[0].AsHeritageClause().Types.Nodes[0].AsExpressionWithTypeArguments().TypeArguments.Nodes[0]
	assert.Equal(t, len(emptyShape.AsTypeLiteralNode().Members.Nodes), 0)
}

func TestEffectScriptSchemaDeclaration(t *testing.T) {
	t.Parallel()
	file := parseETS(t, `
schema Person {
  name: Schema.String
  age:  Schema.Number,
}
`)
	assert.Equal(t, len(file.Diagnostics()), 0, "expected no parse diagnostics")

	stmts := file.Statements.Nodes
	assert.Equal(t, len(stmts), 2)
	assert.Equal(t, stmts[0].Kind, ast.KindImportDeclaration)

	decl := stmts[1].AsClassDeclaration()
	assert.Equal(t, decl.Name().Text(), "Person")
	withTypeArgs := decl.HeritageClauses.Nodes[0].AsHeritageClause().Types.Nodes[0].AsExpressionWithTypeArguments()
	outer := withTypeArgs.Expression.AsCallExpression()
	fields := outer.Arguments.Nodes[0]
	assert.Equal(t, fields.Kind, ast.KindObjectLiteralExpression)
	props := fields.AsObjectLiteralExpression().Properties.Nodes
	assert.Equal(t, len(props), 2)
	assert.Equal(t, props[0].Name().Text(), "name")
	assert.Equal(t, props[1].Name().Text(), "age")

	inner := outer.Expression.AsCallExpression()
	access := inner.Expression.AsPropertyAccessExpression()
	assert.Equal(t, access.Expression.Text(), "Schema")
	assert.Equal(t, access.Name().Text(), "Class")
	assert.Equal(t, inner.Arguments.Nodes[0].Text(), "Person")
	assert.Equal(t, inner.TypeArguments.Nodes[0].AsTypeReferenceNode().TypeName.Text(), "Person")
}

func TestEffectScriptTaggedSchemaContextual(t *testing.T) {
	t.Parallel()
	// tagged / schema / error stay usable as plain identifiers.
	file := parseETS(t, `
const tagged = 1;
const schema = (x: number) => x;
const error = schema(tagged);
const obj = { tagged, error };
tagged
error
`)
	assert.Equal(t, len(file.Diagnostics()), 0, "expected no parse diagnostics")
	for _, s := range file.Statements.Nodes {
		assert.Assert(t, s.Kind != ast.KindClassDeclaration, "no declaration sugar should trigger")
	}
}
