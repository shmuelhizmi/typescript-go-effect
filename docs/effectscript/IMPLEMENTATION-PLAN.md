# EffectScript Implementation Plan

How to implement the language inside this repository (typescript-go). The strategy
copies the JSX integration pattern end-to-end; concrete anchors below were verified
against the current tree.

## Status

| Milestone | State |
| --- | --- |
| Phase 0 — spec + golden corpus | ✅ `docs/effectscript/`, `testdata/tests/cases/effectscript/` |
| Phase 1 — file plumbing (.ets/.etsx, ScriptKinds, `effect`/`effectImportSource` options) | ✅ |
| Phase 2 v0 — `effect` declarations, `effect {}` blocks, binds (`x <- e`, `<- e`, `(<- e)`), `raise`/`raise.die`, auto-import | ✅ parse-time lowering (`internal/parser/effectscript.go`) |
| Phase 2 — service/layer/catch/match/par/race/fork/join/using/defer/`|>`/`raises` sugar, export modifiers, decorator combinators | ✅ |
| Phase 2 acceptance — golden corpus harness (`internal/parser/effectscript_corpus_test.go`, `UPDATE_EFFECT_BASELINES=1` to regen) | ✅ |
| Phase 3+ — dedicated AST kinds + preserve mode, native checking (1810x diagnostics), LSP | ⬜ |

Known v0 deviations / issues (tracked for the native-checking phase):

1. `export default effect f(){}` emits the `Effect.fn` expression directly
   (no local `f` binding), unlike TRANSPILATION §1.1's two-statement form.
2. Or-patterns on object-pattern *fields* (`{ status: 301 | 302 }`) test only
   the first alternative (top-level arm or-patterns work fully).
3. ~~Declaration emit elides the synthesized import~~ — fixed; `.d.ts` output
   retains the import and prints inferred `Effect.Effect<A, E, R>` types
   (synthesized-name fallback in `DeclarationNameToString`).
4. A non-import local binding named like a needed helper (`Fiber`, …)
   collides with the synthesized import (redeclaration error) instead of
   aliasing.
5. Catch/match tag strings are the class *names* (syntax-directed); checker
   resolution of `_tag` comes with native checking.
6. The dedicated 1810x diagnostics are not yet emitted; misuse surfaces as
   ordinary TS parse/type errors.

v0 lowers constructs **at parse time** into plain TS AST (the JSDoc-reparser
pattern) instead of a separate transform pass — the binder/checker/emitter see
standard TypeScript and the `effect` library types do the channel checking. The
dedicated-AST architecture below remains the target for preserve mode and rich
tooling; the parse hooks (`tryParseEffectScriptStatement` & friends) are the seam
where it slots in.

## Architecture decision

**Parse natively, desugar early, check the desugared tree.**

The parser produces dedicated EffectScript AST nodes (good errors, good tooling). A
dedicated transform lowers them to plain TS AST *before* the checker-facing program
is built for `.ets` files' downstream consumers, so:

* the checker needs no new inference — `A`/`E`/`R` checking falls out of checking
  `Effect.gen` / `Effect.fn` calls against the library's types;
* diagnostics re-home to original syntax via `Original` node links + source maps
  (same mechanism the JSX/async transforms use today);
* later (Phase 4+) the checker grows *native* checks for better messages
  (e.g. 18103 "operand of `<-` must be an Effect" instead of a raw `yield*` error).

Exception: **catch/match tag resolution** (mapping a `NotFound >> …` arm to
`catchTag("NotFound", …)` / `Match.tag("NotFound", …)`) needs the checker. v1 keeps it syntax-directed: the tag
string is taken from the class's `Data.TaggedError("X")` / `_tag` declaration
*syntactically* when resolvable in-file, otherwise emitted via a tiny runtime-free
helper pattern `Effect.catchIf((e) => e instanceof C, …)`, which needs no tag at
all. Phase 4 upgrades to checker-resolved tags.

## Touch map (verified file anchors)

| Layer | Files | Work |
| --- | --- | --- |
| Extensions | `internal/tspath/extension.go` (consts at top, supported lists) | add `ExtensionEts = ".ets"`, `ExtensionEtsx = ".etsx"`, extend supported-extension lists |
| ScriptKind | `internal/core/scriptkind.go`, `internal/core/core.go` (`GetScriptKindFromFileName`, ~line 527) | add `ScriptKindETS`, `ScriptKindETSX` + extension mapping |
| LanguageVariant | `internal/core/languagevariant.go` | make variant a flag-style value or add `LanguageVariantEffect`, `LanguageVariantEffectJSX` (JSX scanning rules must stay active for `.etsx`) |
| Options | `internal/core/compileroptions.go` (JSX options ~lines 54–57, `JsxEmit` ~line 530) | add `Effect EffectEmit` (`preserve`/`transform`), `EffectImportSource string`, `GetEffectTransformEnabled()`; register in `internal/tsoptions` declarations |
| Tokens | `internal/ast/kind_generated.go` via `_scripts/ast.json` + `_scripts/generate-go-ast.ts` | add `KindBindArrowToken` (`<-`), `KindPipeForwardToken` (`\|>`), contextual keyword kinds where needed |
| Scanner | `internal/scanner/scanner.go` (variant handling ~line 415/761; precedent: `>>` re-scan) | scan `\|>` as one token under Effect variant; provide `ReScanBindArrow()` for parser-driven `<-` tokenization |
| AST nodes | `_scripts/ast.json` → regenerate (`node --experimental-strip-types _scripts/generate-go-ast.ts`) | new kinds: `EffectDeclaration`, `EffectExpression`, `EffectBlock`, `BindStatement`, `BindExpression`, `RaiseStatement/Expression`, `ServiceDeclaration`, `LayerDeclaration`, `CatchExpression/CatchArm`, `MatchExpression/MatchArm/MatchPattern`, `ParExpression`, `RaceExpression`, `ForkExpression`, `JoinExpression`, `DeferStatement`, `UsingBindStatement`, `PipeExpression`, `EffectTypeSugar` |
| Parser | `internal/parser/parser.go` (JSX functions 4723–5033 as template), `internal/parser/utilities.go` (`getLanguageVariant`, line 11) | statement/expression hooks gated on variant + context flag `InEffectBody`; map new ScriptKinds to variant |
| Binder | `internal/binder` | bind `effect`/`service`/`layer` declarations as value (+type for service) symbols; container handling for effect bodies (they are generator-function-like containers) |
| Transform | new `internal/transformers/effecttransforms/effect.go` (model: `internal/transformers/jsxtransforms/jsx.go`) | the desugarer implementing TRANSPILATION.md, incl. auto-import synthesis (model: JSX implicit import machinery in jsx.go lines 42–100) |
| Pipeline | `internal/compiler/emitter.go` `getScriptTransformers()` (~line 103–171; JSX registration at ~152) | register Effect transformer **before** the type eraser & JSX transform so downstream transforms see plain TS |
| Checker | `internal/checker/` (`checker.go` switch; `jsx.go` as model) | Phase 1: only grammar-context errors (1810x). Phase 4: native checks for binds/raise/catch arms/match patterns |
| Printer | `internal/printer` | print new nodes (needed for `preserve` mode + LSP/formatting round-trip) |
| LSP | `internal/ls`, `internal/lsp` | classify new keywords/tokens for semantic highlighting; completions for `raises`/`requires`; hover on desugared symbols |
| Tests | `testdata/`, `internal/testrunner`, fourslash | golden corpus: `.ets` → emitted JS/d.ts baselines + diagnostics baselines |

## Phases

### Phase 0 — Spec & corpus (this PR)
Docs in `docs/effectscript/`; golden test corpus under `testdata/tests/cases/effectscript/`
(source + expected output pairs) written *against the spec* so later phases have an
acceptance suite before any compiler code exists.

### Phase 1 — File plumbing + tokens (small, mechanical)
Extensions, ScriptKinds, LanguageVariant, compiler options, `<-`/`|>` tokens,
keyword kinds. Everything compiles; `.ets` parses as plain TS still.
Exit criteria: `tsgo` accepts `.ets` files in a program; options round-trip
through tsconfig.

### Phase 2 — Parser + AST
All productions of GRAMMAR.md behind the variant gate, with `InEffectBody` context
flag; parser-level diagnostics (18100–18102, 18120). AST regenerated from
`_scripts/ast.json`. Exit criteria: parse + AST-shape tests green; every spec
example parses; every `.ts` file still parses identically (fuzz: run existing
parser baselines).

### Phase 3 — Desugaring transform + emit
`effecttransforms` implementing TRANSPILATION.md rules 1–10, 12, 13; registration in
the emit pipeline; source-map fidelity. Exit criteria: golden corpus JS output
matches; examples in EXAMPLES.md compile and run against `effect` (integration test
with the real npm package executing transpiled output under Node).

### Phase 4 — Checking
Run checker over desugared trees with original-position re-homing (18103, 18110,
18112, 18130, 18140); checker-resolved `_tag` strings for `catchTag`; `satisfies`
augmentation for `raises`/`requires` annotations (TRANSPILATION §11); declaration
emit for `.ets`.

### Phase 5 — Tooling
LSP features (highlighting, completion, hover, go-to-def through `Original` links,
rename of services/layers), formatter support (dprint plugin config in
`.dprint.jsonc`), `_extension/` VS Code grammar for `.ets`/`.etsx`,
`_packages/native-preview` API surface (new ScriptKinds in the generated enums).

### Phase 6 — Future syntax (spec §15)
Streams comprehensions, `atomic {}` STM blocks, runner sugar.

## Risks & mitigations

1. **`<-` ambiguity** (`a < -b`). Mitigated: BindArrow is only produced at
   grammar-known bind positions via parser-driven re-scan (precedent: `>=`/`>>`
   re-scanning); plain relational parsing is untouched elsewhere.
2. **Contextual keywords breaking real code** (`raise(x)` call in an effect body).
   Carve-outs documented (SPEC §2.2); parser falls back to identifier when the
   keyword production fails its lookahead. Existing `.ts` files are *never*
   affected (extension-gated).
3. **Generated-code checking produces alien errors.** Mitigated by phased plan:
   original-position re-homing first (Phase 4), then targeted native diagnostics.
4. **Arm/block bodies move into nested generators**, changing what
   `break`/`continue`/`return` can reach. Spec'd explicitly (TRANSPILATION §4);
   error 18112 for crossing jumps; `return` in an arm is the recovery value.
5. **Upstream drift** (this repo tracks microsoft/typescript-go). All changes are
   additive and extension-gated; new code lives in new files/dirs where possible
   (`effecttransforms/`, `checker/effect.go`) to minimize merge conflicts.
6. **AST generator constraints** (`_scripts/ast.json` schema limits). Validate early
   in Phase 2 by adding one node kind end-to-end before the rest.

## Versioning

The language version is pinned to the spec draft (`EffectScript 0.x`); each phase
lands gated on the new file extensions; the `effect` option defaults to
`"transform"` (the syntax only exists in `.ets`/`.etsx`, so plain projects are
unaffected), with `"preserve"` as the downstream-tooling escape hatch.
