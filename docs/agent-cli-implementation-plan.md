# tsagent — Technical Implementation Plan

Companion to `docs/agent-cli-spec.md`. This document is the build blueprint: exact internal
APIs to call, package layout, data schemas, and phase-by-phase acceptance criteria.
All signatures below were verified against the codebase at commit `00960bf49`.

---

## 1. Architecture decisions

### 1.1 One-shot first, daemon later

The CLI builds a fresh `compiler.Program` per invocation (Phase 0–6) and gains a
persistent daemon in Phase 7. The command handler layer is transport-agnostic from day
one: every command is a pure function `func(ctx, *core.Workspace, FlagsT) (ResultT, error)`
so the daemon can dispatch the same handlers over JSON-RPC without changes.

### 1.2 Program bootstrap (verified call chain)

```go
fs := bundled.WrapFS(osvfs.FS())                      // internal/bundled, internal/vfs/osvfs
cache := tsoptions.NewExtendedConfigCache()           // internal/tsoptions
config, diags := tsoptions.GetParsedCommandLineOfConfigFile(
    configFileName, nil, nil, parseConfigHost, cache) // internal/tsoptions/tsconfigparsing.go
host := compiler.NewCachedFSCompilerHost(
    cwd, fs, bundled.LibPath(), cache, nil)           // internal/compiler/host.go
program := compiler.NewProgram(compiler.ProgramOptions{
    Config: config, Host: host})                      // internal/compiler/program.go:269
```

- tsconfig discovery: walk up from `--project` dir (or cwd) looking for `tsconfig.json`.
- `ParseConfigHost` needs a small adapter over `vfs.FS` + cwd (see how `cmd/tsgo/sys.go` builds its system; reuse that shape).
- For ad-hoc file sets (no tsconfig): synthesize a `ParsedCommandLine` from defaults — supported but secondary.

### 1.3 Language service bootstrap

`ls.NewLanguageService(projectPath tspath.Path, program *compiler.Program, host ls.Host, activeFile string)`
is standalone — no `internal/project` needed. We implement `ls.Host` once in
`internal/tsagent/core/lshost.go`:

```go
type Host interface {
    UseCaseSensitiveFileNames() bool
    ReadFile(path string) (string, bool)
    Converters() *lsconv.Converters
    GetPreferences(activeFile string) lsutil.UserPreferences
    GetECMALineInfo(fileName string) *sourcemap.ECMALineInfo
    AutoImportRegistry() *autoimport.Registry
    ReadDirectory(cwd, path string, extensions, excludes, includes []string, depth int) []string
    GetDirectories(path string) []string
    DirectoryExists(path string) bool
    FileExists(path string) bool
}
```

- `Converters`: `lsconv.NewConverters(lsproto.PositionEncodingKindUTF8, getLineMap)` —
  we standardize on **UTF-8** encoding so LSP positions equal byte-column positions and
  conversion to our own `file:line:col` is trivial.
- `GetPreferences`: return `lsutil.UserPreferences{}` defaults.
- `AutoImportRegistry`: only needed by completions/import fixes; construct lazily or return an empty registry.
- If any of these prove hard to satisfy, fallback: small exported additions in our fork are allowed (it's a fork — that's the point), but prefer zero-touch.

### 1.4 Checker access rules

- `program.GetTypeChecker(ctx) (*checker.Checker, func())` — generic; call `done()` when finished.
- `program.GetTypeCheckerForFile(ctx, file)` — affinity; **never mix `*checker.Type` values obtained from different checkers**.
- All type-family commands therefore resolve their node first, then use that file's checker for everything in one request.
- For whole-program sweeps (coverage, dead-code), iterate files and use each file's checker.

### 1.5 The Workspace object

`internal/tsagent/core.Workspace` is the per-invocation context every handler receives:

```go
type Workspace struct {
    ConfigPath string
    Cwd        string
    FS         vfs.FS
    Program    *compiler.Program
    LS         *ls.LanguageService
    Conv       *lsconv.Converters
    // helpers
    func (w) FileOf(arg string) (*ast.SourceFile, error)         // path → SourceFile (program-relative resolution)
    func (w) ResolveTarget(t TargetSpec) (*Target, error)        // see 1.6
    func (w) URI(fileName string) lsproto.DocumentUri
    func (w) PosToLineCol(file *ast.SourceFile, pos int) (int,int)
    func (w) LineColToPos(file *ast.SourceFile, line, col int) (int, error)
}
```

Line/col policy: **1-based line, 1-based column, UTF-8 bytes**, rendered `file.ts:12:5`.
Internally everything is byte offsets (`core.TextPos`); conversion via `sourceFile.ECMALineMap()`.

### 1.6 Target resolution & symbol IDs

`TargetSpec` parses three addressing forms:

1. `--at file.ts:12:5` → `astnav.GetTouchingToken(sourceFile, pos)` → node → `checker.GetSymbolAtLocation(node)`.
2. `--symbol 'src/foo.ts#Class.method'` → symbol ID decode: load file, walk its symbol tree by qualified-name segments (module exports → members), pick first declaration.
3. `--name X [--kind k]` → workspace symbol search (`map search` machinery), error if ambiguous unless `--all`.

Symbol ID encoding: `relpath#seg1.seg2` where segments are declaration names from the
module root; disambiguator suffix `~N` for merged/overloaded same-name decls (index into
`symbol.Declarations`). Implemented in `core/symbolid.go` with `Encode(symbol) string` and
`Decode(ws, id) (*ast.Symbol, *ast.Node, error)`. Encode walks `symbol.Parent` chain up to
the source-file symbol; falls back to position-encoding `relpath@pos` for locals.

`Target` carries: `Node *ast.Node`, `Symbol *ast.Symbol`, `File *ast.SourceFile`, `Pos int`.

### 1.7 Output layer

`internal/tsagent/cli/output.go`:

- Every command returns a Go value; the CLI layer marshals it.
- `--format json` (default when piped) / `text` (default when TTY) / `ndjson` for list results.
- Each result type implements `Texter interface{ WriteText(w io.Writer, opts TextOpts) }` for the compact format; JSON via struct tags. `schemaVersion: 1` injected at top level.
- Pagination: `--limit/--offset` handled generically by the CLI layer for results implementing `Lister` (returns items + total). Truncation always reported.
- Exit codes per spec §2.5 via typed errors (`cli.ErrNotFound`, `cli.ErrRefused`, `cli.ErrPartial`).

### 1.8 Command registry

`internal/tsagent/cli/registry.go`:

```go
type Command struct {
    Family, Name string               // "nav", "refs"
    Summary      string
    Flags        func(fs *flag.FlagSet) any        // returns flags struct
    Run          func(ctx context.Context, ws *core.Workspace, flags any, args []string) (any, error)
    NeedsProgram bool                              // false for pure commands (e.g. serve admin)
}
func Register(c Command)              // called from init() in cmds files
```

Each command family lives in its own file under `internal/tsagent/cmds/` and self-registers
via `init()`. **This is what makes parallel implementation safe**: adding a family touches
only new files. `cmd/tsagent/main.go` imports `internal/tsagent/cmds` for side effects,
parses `tsagent <family> <name> [flags] [args]`, builds the Workspace, dispatches.

### 1.9 Edits & transactions

`internal/tsagent/core/edits.go`:

- Canonical edit form: `FileEdit{FileName string, Edits []core.TextChange}` (+ file-level ops: create/delete/rename).
- From LSP: convert `lsproto.TextEdit` (range) → `core.TextChange` via Converters; `lsproto.WorkspaceEdit.Changes` map → `[]FileEdit`.
- Apply: `core.ApplyBulkEdits(text, edits)` (verified: `internal/core/textchange.go:14`; requires sorted, non-overlapping — sort + validate first).
- Dry-run diff: compute per-file before/after, render unified diff with a small internal differ (line-based LCS, ~100 lines, no new deps).
- Transaction: write all files to temp, validate, then rename into place; on `--apply` without `--allow-errors`, rebuild program from edited contents in-memory **first** (overlay FS wrapper, see 1.10), compare diagnostics counts, refuse (exit 4) if new errors.

### 1.10 Overlay FS for speculative checks

`internal/tsagent/core/overlayvfs.go`: a `vfs.FS` decorator holding
`map[path]string`; `ReadFile`/`FileExists`/`Stat` consult overlays first, everything else
delegates. Speculative check = build second Program with overlay FS + same config, diff
diagnostics. (We do NOT reuse `internal/project` overlayFS — it is unexported and coupled
to LSP sessions; ours is ~80 lines.)

Diagnostic identity for delta: `(file, code, range.pos, range.end, message)` — match
`fixed` = in base not in new; `new` = in new not in base; rest unchanged. Positions shift
under edits, so also do fuzzy match on `(file, code, message)` to avoid reporting moved
diagnostics as new+fixed pairs (classify as `moved`, count as unchanged).

### 1.11 Daemon (Phase 7)

ndjson JSON-RPC 2.0 over stdio (`tsagent serve --stdio`) or unix socket
(`--socket <path>`, default `$TMPDIR/tsagent-<hash(configPath)>.sock`).
Method namespace mirrors CLI: `"nav/refs"`, params = flags JSON. Daemon holds
`Workspace` + mtime-based invalidation: before each request, stat known files; if changed,
rebuild program (full rebuild is acceptable; `UpdateProgram` single-file fast path used
when exactly one file changed: `program.UpdateProgram(changedFilePath, newHost, nil)`).
Overlay methods mutate the overlay map and force rebuild. CLI auto-connect deferred
(explicit `--connect` flag instead) to keep scope contained.

---

## 2. Package layout

```
cmd/tsagent/main.go                  — entry; flag routing; Workspace construction
internal/tsagent/
  cli/registry.go                    — Command, Register, dispatch
  cli/output.go                      — json/text/ndjson writers, pagination, exit codes
  cli/textfmt.go                     — text-format helpers (indent trees, tables)
  core/workspace.go                  — Workspace bootstrap (1.2, 1.3, 1.5)
  core/lshost.go                     — ls.Host implementation
  core/target.go                     — TargetSpec parse + resolve (1.6)
  core/symbolid.go                   — symbol ID encode/decode
  core/edits.go                      — FileEdit, conversion, apply, transactions (1.9)
  core/diff.go                       — unified diff renderer + parser (for --with-diff)
  core/overlayvfs.go                 — overlay FS (1.10)
  core/graph.go                      — import graph build, Tarjan SCC, BFS paths
  cmds/mapcmd.go                     — map outline|search|files|stats
  cmds/nav.go                        — nav def|refs|usages|calls|graph|path
  cmds/typecmd.go                    — type at|assignable|coverage|complexity|instantiations
  cmds/refactor.go                   — refactor rename|mv|organize-imports|safe-delete
  cmds/analyze.go                    — analyze dead-code|assertions|unused-deps|complexity|exhaustiveness
  cmds/diagram.go                    — diagram deps|classes|calls
  cmds/check.go                      — check, check --with-diff
  cmds/apicmd.go                     — api surface|diff
  cmds/serve.go                      — serve (daemon), serve overlay admin
  *_test.go alongside each           — vfstest-based unit tests
testdata/tsagent/fixture/            — small TS project for integration tests
```

Module path additions: none (same module `github.com/microsoft/typescript-go`).
Build: `go build ./cmd/tsagent`. Tests guarded by `bundled.Embedded` skip like the rest of the repo.

---

## 3. Per-command implementation notes

### map outline
Walk `sourceFile.Statements` recursively collecting declaration nodes (class, interface,
enum, function, type alias, var statements, module decls; members within). Do NOT use
`ProvideDocumentSymbols` (LSP-shaped, loses symbol identity); build our own entries:
`{name, kind, symbolId, exported, line range, signature}`. Signature one-liner: for
functions use `checker.SignatureToString` on declaration signature; for vars
`checker.TypeToString` capped. `--exported-only` checks `ast.SymbolFlags` / modifier flags.
Folder mode: iterate `program.SourceFiles()` filtered to inputs, respecting `--depth`.

### map search
Reuse `ls.ProvideWorkspaceSymbols(ctx, []*compiler.Program{p}, conv, prefs, query)` for
fuzzy match, then re-resolve each hit to attach symbolId; or implement directly over
file symbol tables (preferred: full control of filters). Direct impl: for each source file,
walk module exports + locals via binder symbol tables (`sourceFile.AsNode().Symbol()`...),
fuzzy score = `ls.getMatchScore` reimplemented (~20 lines).

### map files / map stats
`program.SourceFiles()`, classify (lib/declaration/external via path + `file.IsDeclarationFile`).
`--why`: use `program.GetResolvedModules()` reverse map — build importer index once.

### check
`program.GetSyntacticDiagnostics/GetSemanticDiagnostics/GetSuggestionDiagnostics(ctx, file)`
per file (nil file = all? — call per file to enable path filtering). Map `ast.Diagnostic`
→ our schema `{file, range{line,col..}, code, category, message, related[]}`. Filters
`--severity --code --path-glob`. `diagRef` = `file:pos:code` (stable within run).

### nav def
`ls.ProvideDefinition(ctx, uri, position)`; convert response locations back to
`file:line:col` via Converters. `--implementations` → `ProvideImplementations` (needs
`orchestrator CrossProjectOrchestrator` — pass nil/no-op; verify nil-safety, else minimal stub).

### nav refs / usages
`ls.GetReferencedSymbolsForNode(ctx, pos, node, sourceFiles)` returns raw
`[]*SymbolAndEntries`. Classify each `ReferenceEntry`: write access (parent is assignment
LHS / declaration), import, type-position (`ast.IsPartOfTypeNode`), call
(parent CallExpression callee). usages adds `--context-lines` source excerpts.

### nav calls
`ProvidePrepareCallHierarchy` + `ProvideCallHierarchyIncomingCalls/OutgoingCalls`,
recursing to `--depth` with visited-set cycle marking.

### nav graph / path / diagram deps
Build once in `core/graph.go`: for each file, `file.Imports()` (module specifier nodes) +
`program.GetResolvedModuleFromModuleSpecifier(file, spec)` → edges. Tarjan SCC for
`--cycles`; BFS for `nav path --via imports`. `--via calls` path: BFS over call hierarchy
expansion (bounded depth 32).

### type at
Node → file checker → `c.GetTypeAtLocation(node)`. Display:
`c.TypeToStringEx(t, node, TypeFormatFlagsNoTruncation|defaults, nil)`. Structural JSON:
recursive encoder over `t.Flags()`: union → `c.…Types()` members, object →
`c.GetPropertiesOfType` (name + `c.GetTypeOfSymbol` display, depth-limited by
`--expand-depth`, default 1), signatures via `c.GetSignaturesOfType(t, SignatureKindCall)`
(params + `c.GetReturnTypeOfSignature`), type args via `c.GetTypeArguments`.

### type assignable
Resolve two targets in the **same file's checker** when possible; if cross-file, resolve
both through `program.GetTypeChecker(ctx)` (the generic checker) by re-resolving nodes.
`c.IsTypeAssignableTo(s, t)`. Elaboration chain: not exposed → on failure, do a one-level
property-wise drill: for each property of target, check source property presence +
assignability, recurse depth ≤ 3. (Good enough for v1; note as approximation in output.)

### type coverage
Walk each file's AST; for expression nodes (identifier refs, call exprs, property access)
get type, count `TypeFlagsAny|Unknown` (skip declared `any`). Report per file + total.
Also count `AsExpression`/`NonNullExpression`/`@ts-ignore` (scan comments) for the
assertions inventory shared with `analyze assertions`.

### type complexity
Pure function over `*checker.Type` with memo + depth cap:
`cost(t) = 1 + Σ cost(unionMembers) + Σ cost(typeArgs) + propertyCount/heuristics`;
flags-driven: conditional/mapped/indexed-access get multipliers. Per-type-alias and
per-exported-symbol report, `--rank` sorts project-wide.

### refactor rename
`ls.ProvideRename(ctx, &lsproto.RenameParams{...}, nil)` → `WorkspaceEditOrNull` →
FileEdits → transaction engine.

### refactor mv
`ls.GetEditsForFileRename(ctx, oldURI, newURI)` → edits +
actual `git mv`-style rename (plain os.Rename; caller's git picks it up as rename).
Multiple sources: sequential, single transaction.

### refactor organize-imports
`ls.OrganizeImports(ctx, sourceFile, program, kind)` returns `map[string][]*lsproto.TextEdit` → transaction.

### refactor safe-delete
Target → refs (excluding declaration + same-decl internal refs) → if any remain: exit 4
with blocking list. Delete = remove declaration's full range (incl. leading trivia/JSDoc)
via TextChange; `--cascade`: fixpoint loop re-running dead check on symbols the deleted
decl referenced.

### analyze dead-code
Roots: exported symbols of entry files (`--entry`, default: all non-declaration files'
exports if library-mode flag absent → use package.json main/exports if present, else all).
Mark phase: BFS over references from roots? Cheaper inversion: for every declaration
symbol in project, run reference search and count non-self refs. That is O(symbols ×
search) — too slow for big projects; mitigation: use the binder's symbol tables and an
identifier-index: single pass over all files collecting identifier→files index, then for
each candidate symbol only search files containing its name (the same trick the real
find-refs uses internally). v1: per-file locals + unexported module members (cheap,
high-confidence), exports behind `--include-exports` with the name-index optimization.

### analyze exhaustiveness
For each switch statement: type of discriminant via checker; if union of literals/enum:
collect case clause literal values, diff. Report missing variants (needs no fix engine in v1).

### analyze unused-deps
Parse package.json deps; collect all resolved module specifiers that land in
node_modules/<pkg>; set-diff both directions.

### diagram classes
For each class/interface decl: heritage clauses (`ast.GetExtendsHeritageClauseElement` etc. or
walk HeritageClause nodes), emit mermaid `classDiagram`. Members at `--members signatures`.

### api surface
For each entry file: module symbol → `c.GetExportsOfModule` → for each export:
`c.SymbolToStringEx` + `c.TypeToStringEx(c.GetTypeOfSymbol(s), …, NoTruncation, …)`,
sorted, hashed. `api diff`: run surface at `--base` via `git show <ref>:<file>` into
overlay FS (reuses 1.10!) — no checkout needed. Classify: removed export = breaking;
type-string change = possibly-breaking (v1 string compare; structured compare later);
added = additive.

### check --with-diff
`core/diff.go` parses unified diff → per-file new content → overlay FS → second program →
delta per 1.10.

---

## 4. Testing strategy

- Unit tests colocated, using `vfstest.FromMap` + the 1.2 bootstrap against in-memory
  files; guard with `if !bundled.Embedded { t.Skip(...) }` (repo convention).
- One shared helper `internal/tsagent/core/testws_test.go`: `newTestWorkspace(t, files map[string]any) *Workspace`.
- Integration: `testdata/tsagent/fixture` real-disk small project; `cmd/tsagent` e2e test
  invoking handlers directly (not subprocess) + one subprocess smoke test.
- Gate per phase: `go build ./... && go vet ./internal/tsagent/... ./cmd/tsagent && go test ./internal/tsagent/... ./cmd/tsagent/...`
- Formatting: repo uses dprint for non-Go; Go code: `gofmt`.

## 5. Phase plan & acceptance criteria

| Phase | Deliverable | Accept when |
|---|---|---|
| 0 | core/, cli/, map *, check | build+vet+tests green; `tsagent map outline` and `tsagent check` work on fixture |
| 1 | nav * | refs/def/calls/graph/path tests green on fixture incl. cycle detection |
| 2 | type * | type at/assignable/coverage/complexity tests green |
| 3 | transactions + refactor * | rename dry-run shows diff; apply mutates fixture copy; gate refuses error-introducing edit |
| 4 | analyze * | dead-code finds planted dead symbol; exhaustiveness finds missing case |
| 5 | diagram * | mermaid output snapshot tests |
| 6 | check --with-diff, api * | speculative delta correct on planted diff; api diff classifies planted break |
| 7 | serve | RPC round-trip test: same handler results as one-shot |
| 8 | docs+integration | e2e suite green; README complete; branch committed per phase |

Each phase ends with a commit on branch `tsagent`:
`tsagent: phase N — <summary>`.

## 6. Risks & fallbacks

- **ls.Host construction friction** (AutoImportRegistry, ECMALineInfo): if constructors are
  unexported/heavy, add minimal exported shims in the fork (`internal/ls/lsutil` etc.) — smallest possible diff, documented in plan.
- **CrossProjectOrchestrator nil-safety**: check call sites; if not nil-safe, implement no-op orchestrator.
- **Perf on dead-code**: keep `--include-exports` clearly slower path; document.
- **UpdateProgram API drift**: daemon falls back to full `NewProgram` rebuild — correctness first.
- **lsproto position encoding**: we force UTF-8 in our Converters; LSP types still carry
  line/character — converting through `Converters` everywhere avoids drift.
