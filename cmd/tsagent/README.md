# tsagent — TypeScript language service CLI for agents

`tsagent` is an agent-oriented command-line interface to this repository's native TypeScript compiler and language service. It answers the questions coding agents actually ask — "what is in this project?", "who calls this?", "what type is this?", "is it safe to rename/delete this?", "what breaks if I apply this diff?" — as single shell commands with compact, LLM-optimized text output (JSON on demand). Mutating commands are transactional: `refactor` commands dry-run by default, print a unified diff, and refuse to apply edits that would introduce new type errors; the batched `edit` script language applies by default behind the same gate. A session daemon (`tsagent serve`) keeps the program warm for repeated queries on large projects.

- Feature specification: [`docs/agent-cli-spec.md`](../../docs/agent-cli-spec.md)
- Architecture / implementation plan: [`docs/agent-cli-implementation-plan.md`](../../docs/agent-cli-implementation-plan.md)

## Install

From the repository root:

```sh
go build -o ~/.local/bin/tsagent ./cmd/tsagent
```

(Any directory on your `PATH` works. The binary embeds the TypeScript lib `.d.ts` files; it is fully self-contained.)

## Quickstart

All examples below are real output against the test fixture project (`testdata/tsagent/fixture`, a small strict-mode project with a planted type error, an import cycle, a non-exhaustive switch, a structural clone pair, and a discriminated-union state machine). Run `tsagent` from anywhere inside a project — the nearest `tsconfig.json` is discovered automatically, or pass `--project <tsconfig|dir>`. A `--project` directory without its own `tsconfig.json` walks up to the nearest ancestor config (note: the project root — and therefore all displayed/accepted relative paths — is then the directory of that ancestor config).

**1. Orient yourself — outline a file (or a whole directory):**

```
$ tsagent map outline src/models.ts
src/models.ts
  interface Animal  [export]  (4-7)
    property name  : string  (5-5)
    method speak  (): string  (6-6)
  class Dog  [export]  (9-15)
    constructor constructor  new (name: string): Dog  (10-10)
    method speak  (): string  (12-14)
  class Puppy  [export]  (17-21)
  class Container  [export]  (23-33)
    method get  (): T  (26-28)
    method set  (value: T): void  (30-32)
```

**2. Check diagnostics, with filters:**

```
$ tsagent check --severity error
src/broken.ts:4:14 error TS2322: Type 'string' is not assignable to type 'number'.
```

**3. Find a symbol, then chase its references by symbol ID:**

```
$ tsagent map search Dog
class Dog  src/models.ts:9:14  [export]  src/models.ts#Dog

$ tsagent nav refs --symbol "src/models.ts#Dog"
src/index.ts
  5:21 import  import { Container, Dog, Puppy } from "./models";
  11:21 call  const dog = new Dog("Rex");
src/models.ts
  17:28 read  export class Puppy extends Dog {
total: 3  call:1 import:1 read:1
```

**4. Rename safely — dry-run prints the diff, `--apply` writes it:**

```
$ tsagent refactor rename --name Dog Hound
--- a/src/index.ts
+++ b/src/index.ts
@@ ...
-import { Container, Dog, Puppy } from "./models";
+import { Container, Hound, Puppy } from "./models";
--- a/src/models.ts
+++ b/src/models.ts
@@ ...
-export class Dog implements Animal {
+export class Hound implements Animal {
-export class Puppy extends Dog {
+export class Puppy extends Hound {
dry-run: 2 file(s) would change (pass --apply to write)
```

**5. Visualize module dependencies (cycles highlighted in red):**

```
$ tsagent diagram deps
flowchart LR
  src_cycle_b_ts["src/cycle-b.ts"]
  src_cycle_a_ts["src/cycle-a.ts"]
  ...
  src_cycle_b_ts --> src_cycle_a_ts
  src_cycle_a_ts --> src_cycle_b_ts
  src_index_ts --> src_models_ts
  linkStyle 0 stroke:#cc0000,stroke-width:2px
  linkStyle 1 stroke:#cc0000,stroke-width:2px
```

## Output formats

| Flag | Effect |
|---|---|
| (default) | Compact, token-efficient text designed for LLM consumption |
| `--raw` | Structured JSON (shorthand for `--format json`; the two flags are mutually exclusive) |
| `--format json` | JSON envelope: `{"schemaVersion": 1, "result": …}` for single results, `{"schemaVersion": 1, "total", "offset", "count", "truncated", "items": […]}` for lists |
| `--format ndjson` | List results: one JSON object per line, followed by a trailing envelope line with totals |
| `--format text` | Explicit default |

**Pagination.** List-shaped results (`check`, `map search`, `map files`, …) honor the global `--limit N` and `--offset N` flags. Truncation is never silent — text output appends `(showing N of M results; use --limit/--offset to page)` and JSON sets `"truncated": true`.

**Empty results are never silent.** In text mode a clean `check` prints `0 diagnostics`, a no-hit `map search` (and every other list command) prints `0 results`, and summary-style results report their zero explicitly (`nav refs` prints `total: 0`) — so "nothing found" is always distinguishable from "no output".

**Exit codes**

| Code | Meaning |
|---|---|
| 0 | Success (note: `check` exits 0 even when diagnostics are found — diagnostics are data) |
| 1 | Operation failed (internal error, bad project, threshold exceeded, `--fail-on` tripped) |
| 2 | Invalid arguments |
| 3 | Target not found (symbol/file/position resolves to nothing) |
| 4 | Mutation refused (would introduce new errors; see Transactions) |
| 5 | Partial success (some batch items failed) |

## Addressing targets

Most commands accept a *target* in any of three forms:

1. **Position** — `file:line:col` (1-based line and column), e.g. `src/index.ts:11:21`.
2. **Symbol ID** — `path#qualified.name`, e.g. `src/models.ts#Container.get`. Symbol IDs are printed by `map search`, `map outline --raw`, `nav calls`, and others; paths are always relative to the project root (the tsconfig directory), so IDs are stable regardless of your cwd. The qualified name extends through every *named scope* — functions, classes, namespaces, methods, `const f = () => {}`-style declarators, and **named class/function expressions** (the factory pattern `export const f = (cfg) => class Name extends Base {…}` yields `src/x.ts#f.Name` and `src/x.ts#f.Name.method`, whether the class is the arrow's expression body, a `return class Name …`, or a declarator initializer) — so even function-local declarations have guessable IDs like `src/server.ts#startServer.logRequest` (blocks such as `if`/`for`/`try` are transparent; `map outline --locals` lists them). Ambient module declarations use quoted segments: `src/types.d.ts#"virtual-thing".vt`.

   **`~N` ordinals.** Whenever several declarations share one name path — merged declarations (`interface Foo` + `namespace Foo` + `function Foo`), getter/setter pairs, overload signatures, duplicate `var`s, or same-name declarations in sibling scopes — a 0-based **source-order** ordinal distinguishes them (`src/m.ts#Foo~0`, `…~1`, `…~2`), printed IDs always carry it, and `map outline` shows it on duplicate same-name rows. A bare ID over such a merged symbol is an error listing the `~N` alternatives with their kinds and lines — **except** function/method/constructor overload groups, where the bare ID addresses the whole group (each `~N` still addresses one signature). An out-of-range ordinal errors with the valid range (`ordinal ~7 out of range for src/m.ts#x (2 declarations: ~0..~1)`).

   Only declarations under *anonymous* scopes (callbacks, IIFEs, anonymous class expressions) and computed-name members (`[Symbol.iterator]() {…}`) use the fallback form `path@bytePos`. A `@pos` that lands in trivia — a leading comment, blank lines between declarations, EOF — errors with the file's top-level declaration IDs as suggestions; it never silently resolves to the whole file.
3. **Name** — `--name <identifier>` resolves a declaration by name project-wide; it must be unambiguous (exits 3 when there are no matches, errors when there are several — disambiguate with `--kind` where supported, or use a symbol ID).

Refactor commands also accept the target as the first positional argument and guess the form (`file:line:col` → position, contains `#`/`@` → symbol ID, otherwise name).

## Command reference

Global flags (valid on every command, anywhere after `<family> <command>`):
`--project <tsconfig|dir>` · `--raw` · `--format json|text|ndjson` · `--limit N` · `--offset N` · `--connect never|auto|require` (route through a running `tsagent serve` daemon; see the daemon section) · `--socket-path <path>` (the daemon socket `--connect` dials, for daemons started with a custom `serve --socket-path`; default: derived from the resolved tsconfig)

### `edit` — batched symbol-edit scripts

The flagship editing primitive for LLMs: a terse, line-based script language that batches many structural edits — reorder, move across files, insert raw code at symbol-relative positions, replace, delete — into **one invocation and one atomic transaction**. The whole script is validated against the original program first (every bad symbol ID and every conflicting op is reported at once, with line numbers), then applied as a single edit set behind the usual diagnostics gate.

> **⚠ `edit` APPLIES BY DEFAULT** — the opposite of the `refactor` family. The type-check gate still refuses scripts that would introduce new errors (exit 4, disk untouched), and `--dry-run` previews the per-op results and diffs without writing. `--allow-errors` bypasses the gate.

```
tsagent edit <script-file|->                 # read the script from a file or stdin
tsagent edit -e '<op line>' [-e '<op line>'…]  # inline ops, joined with newlines
flags: --dry-run, --allow-errors, --strict-gate
```

**Grammar** (line-based; `#` comments; raw code via heredocs `<<TAG … TAG`, verbatim, no interpolation):

```
move    <sym> before|after <sym> [with-deps]  # reorder within a file, or cross-file with an anchor
move    <sym> top|end <path>     [with-deps]  # cross-file move to a file-level position
insert  before|after <sym> <<EOF … EOF  # raw code at a symbol-relative position
insert  top|end <path>     <<EOF … EOF
insert  into <class-like-sym> <<EOF … EOF   # append as the last member (class/interface/enum/namespace)
replace <sym> <<EOF … EOF               # replaces the full declaration INCL. leading JSDoc and // comments
delete  <sym>
```

**Symbol IDs are guessable.** The ID of any declaration is `path#<names of enclosing functions/classes/namespaces/named-arrow consts, dot-joined>.<name>`; append `~N` (0-based, source order) when several declarations share that path — a bare ID over merged declarations errors listing the `~N` candidates, except function/method overload groups, where the bare ID means **the whole group** (`delete` removes every signature, `replace` replaces the contiguous group — non-contiguous groups must be replaced per `~N` — within-file `move` moves the block, and `insert before|after` anchors on the group's first/last declaration). An LLM that has read a file can construct every ID without running a tool (`map outline --locals` prints them all). Unknown IDs fail with closest-match suggestions: `line 1: unknown symbol src/a.ts#helpr; closest: src/a.ts#helper`.

A worked script (one transaction; `tsagent edit tidy.edit` or pipe to `tsagent edit -`):

```
# tidy server.ts
move src/server.ts#Router before src/server.ts#startServer
insert after src/server.ts#startServer <<EOF
/** Gracefully stops the server. */
export function stopServer(s: Server) {
  return s.close();
}
EOF
insert into src/server.ts#Router <<EOF
  remove(path: string) {
    this.routes = this.routes.filter(r => r.path !== path);
  }
EOF
replace src/server.ts#startServer.logRequest <<EOF
function logRequest(req: Request) {
  console.log(req.method, req.url);
}
EOF
delete src/server.ts#legacyHandler
move src/server.ts#Router end src/router.ts
```

Output: one `ok line N: <op>` per op (cross-file move notes in parens), the unified diffs, and a summary — `applied: 4 file(s) changed, 0 new errors, 1 fixed` (or `dry-run: 4 file(s) would change`). `--raw` returns `{"ops": [{"line", "op", "files", "notes"}…], "tx": {…}}`.

**Semantics.** All byte offsets address the *original* file text (an `insert after X` anchored on a symbol the same script moves lands at X's original location). Within-file `move` and same-class member reorders are pure text moves — the trivia-aware declaration range travels with its leading JSDoc **and contiguous leading `//` line comments** (no blank line between the comment block and the declaration ⇒ the comments belong to it: they travel with moves, vanish with `delete`/`replace`, and `insert before X` lands *above* them; a blank line detaches them, so file headers stay put). Cross-file `move` (anchored `before`/`after` a symbol in another file, or `top`/`end <path>`) reuses the `mv-symbol` planner: imports and `export { X } from` re-exports are rewritten in every consumer (a consumer that imported through a bare package specifier is retargeted to the destination package's `exports` subpath when one maps, never a relative path crossing its package boundary — see the mv-symbol notes under *Known limitations*), the declaration is exported in the destination when needed, and the source re-imports it if it still uses it — with the same refusals (overloads, default exports, file-local unexported deps unless the move line ends with `with-deps`, which moves the transitive local helpers along, unexported, deps-first); `with-deps` on a within-file move has no effect and says so in a note. Members cannot move across containers (`use delete + insert into`), function-local declarations cannot move, and `edit` never creates files (use `refactor mv-symbol --create`).

**Enum members.** `insert into` an enum body is comma-aware: the inserted text lands after the last member's trailing comma (on its own line), and when the last member has no trailing comma the separating `,` is added to it automatically — both comma styles produce valid code. The heredoc body itself is raw; commas *inside* it (including the new last member's optional trailing comma) are the caller's choice. Class, interface, and namespace bodies are newline-separated and need no separator handling.

**Ops compose.** `delete X` + `insert after X` (or `insert before X`) in one script compose: the inserted code lands exactly where X was — after-X content at the deleted range's end, before-X content at its start. Genuinely conflicting ops (two ops rewriting the same bytes, `insert into` a `replace`d container) are refused with line attribution before anything runs. Heredoc bodies must be non-empty (`line N: empty body`, exit 2) and are rewritten to the target file's dominant line endings, so inserting into a CRLF file never produces mixed EOLs.

**Blank-line hygiene.** Top-level `insert before|after|top|end` ops and the re-insertion half of moves keep exactly one blank line between the inserted block and the adjacent declaration, and a `delete` (or the vacated spot of a move) that would leave a double blank line eats one extra newline — including at the very top of the file (deleting the first declaration leaves no leading blank line). `insert top` (and rewritten/prepended imports everywhere) lands **below** a shebang and the directive prologue, so a leading `"use client";` / `"use strict";` keeps its meaning. When a delete or cross-file move empties a file (nothing but whitespace left), the file itself is deleted and a `note:` reports it.

**Exit codes:** 2 — syntax errors, illegal ops, or overlapping ops (`line 6: delete src/a.ts#f conflicts with replace at line 1 (overlapping ranges in src/a.ts)`); 3 — unresolvable symbols/paths (all reported together, with suggestions; ambiguous bare IDs list their `~N` alternatives); 4 — diagnostics-gate refusal (nothing written; the new errors are listed).

### `map` — orientation

| Command | Description | Flags |
|---|---|---|
| `map outline <path…>` | Symbol tree per file/folder with ranges and signatures | `--depth N\|top-level\|all` (default `all`), `--exported-only`, `--kind class,interface,function,…`, `--detail names\|signatures\|full` (default `signatures`; `full` adds the first JSDoc line), `--with-ref-counts` (with `--detail full`, approximate ref counts for top-level exports; expensive), `--locals` (descend into function bodies and list local declarations with their scoped IDs), `--symbol <id>` (print only that symbol's subtree; implies `--locals` within it) |
| `map search <query>` | Project-wide fuzzy symbol search returning symbol IDs | `--kind …`, `--exported-only`, `--path-glob <glob>` |
| `map files` | Program file inventory with classification (source/lib/declaration) | `--why <file>` (explain why a file is in the program: root / lib / shortest import chains from root files), `--max-chains N` (default 3) |
| `map stats` | Per-directory counts of files, lines, symbols, exports | — |

Path arguments that select no program files (here and in every other path-taking command) fail with exit 3 and say *why*: `scripts/x.ts exists but is not included by the project tsconfig` for a real file the project leaves out, `src/nope.ts: no such file or directory` for a typo.

### `nav` — navigation & graph queries

| Command | Description | Flags |
|---|---|---|
| `nav def <target…>` | Go to definition (batch) | `--symbol`, `--name`, `--implementations`, `--type-definition` |
| `nav refs <target>` | All references with per-reference usage kinds | `--symbol`, `--name`, `--include-declaration`, `--kind declaration,import,type,call,write,read`, `--group-by file\|kind` |
| `nav usages <target>` | References grouped per file with context excerpts | `--symbol`, `--name`, `--context-lines N` (default 2) |
| `nav calls <target>` | Call hierarchy tree with cycle marking | `--symbol`, `--name`, `--direction in\|out\|both` (default `in`), `--depth N` (default 3) |
| `nav graph` | Module dependency graph with cycle detection | `--scope file\|dir`, `--cycles` (SCCs only), `--externals` |
| `nav path --from <file> --to <file>` | Shortest import chain(s) between two files | `--from`, `--to`, `--max-paths N` |
| `nav types <target>` | Type hierarchy of a class/interface: supertypes (`extends`/`implements`) and subtypes | `--symbol`, `--name`, `--direction up\|down\|both` (default `both`), `--depth N` (0 = unlimited), `--structural` (include implicit structural implementers) |
| `nav shape '<pattern>'` | Find functions matching a signature pattern `(<type>, …) => <type>`; `*` is a wildcard, trailing `, ...` allows extra params | `--kind function,method,arrow`, `--exported-only`, `--path-glob <glob>` |
| `nav export-graph <target>` | Re-export/barrel flow of a symbol: every re-export hop and public name it is reachable as | `--symbol`, `--name` |

`nav types` against the fixture hierarchy (`Dog implements Animal`, `Puppy extends Dog`):

```
$ tsagent nav types --name Dog
Dog  src/models.ts:9  src/models.ts#Dog
up
  Animal  src/models.ts:4  src/models.ts#Animal  implements
down
  Puppy  src/models.ts:17  src/models.ts#Puppy  extends
```

### `type` — type intelligence

| Command | Description | Flags |
|---|---|---|
| `type at <target…>` | Resolve the type at positions/symbols, structurally expanded | `--expand-depth N` (default 1), `--symbol`, `--name` |
| `type assignable --source <target> --to <target>` | Assignability check with a property-wise drill-down on failure | `--source`, `--to` |
| `type coverage [path…]` | `any`/`unknown` expressions, casts, non-null assertions, ts-ignores per file | `--threshold <pct>` (exit 1 when project any% exceeds it) |
| `type complexity [path…]` | Structural complexity of exported type aliases/interfaces/classes | `--symbol`, `--threshold N`, `--rank` (default true), `--top N` (default 50) |
| `type instantiations <generic-target>` | Best-effort list of concrete instantiations of a generic (via find-references) | — |
| `type explain-error <file:line:col \| diagRef>` | Decompose a diagnostic into its causal message chain, plus a property-wise assignability drill-down | — |
| `type flow <target>` | Control-flow narrowing trace for a variable: its flow type at every reference in the declaring file | — |
| `type infer [path…]` | Suggest types for unannotated parameters and returns (usage- and inference-based) | `--symbol <target>` (one function instead of paths), `--fix-plan` (emit an annotation edit set for `refactor apply-edits`) |

A *diagRef* is the stable diagnostic handle `file:bytePos:TSnnnn` printed by `check` (JSON output) and `type explain-error`; `check fix` consumes them too.

`type flow` over the narrowed switch in the fixture:

```
$ tsagent type flow src/shapes.ts:23:31
shape  (declared: Shape)  src/shapes.ts
    23:31   export function describeShape(shape: Shape): string {  Shape  [declaration]
    24:13   switch (shape.kind) {                                  Shape
    26:32   return `circle r=${shape.radius}`;                     Circle  (narrowed from Shape)
    28:32   return `square s=${shape.side}`;                       Square  (narrowed from Shape)
```

### `perf` — type-system performance insights

Each `perf` command runs a fresh, fully traced compile of the project entirely in memory (no trace files are written to disk), then aggregates the trace, the recorded type descriptors, and the compiler statistics into agent-actionable rankings. The ranking commands default to the user's own files; pass `--include-libs` to fold in bundled libs and `node_modules`. The `perf` family is text-only; for the HTML performance report see [`report perf`](#report--multi-page-html-reports).

| Command | Description | Flags |
|---|---|---|
| `perf summary` | Phase budget (parse/bind/check/emit split), project counters, and a one-line bottleneck verdict | `--emit`, `--single-threaded` |
| `perf hot-files` | Files ranked by compiler time (parse/bind/check) and recorded type count | `--top N` (default 50), `--include-libs`, `--emit`, `--single-threaded` |
| `perf hot-types` | Generics/aliases ranked by how many distinct types they instantiate (instantiation spread), with conditional/union signals | `--top N` (default 50), `--include-libs`, `--emit`, `--single-threaded` |
| `perf hot-checks` | Individual checker operations the tracer sampled as slow (>~10ms), located back to a file | `--top N` (default 50), `--emit`, `--single-threaded` |
| `perf depth-limits` | Type explosions that tripped an instantiation/recursion/union-size guard — the highest-value fixes | `--emit`, `--single-threaded` |

Shared flags: `--emit` also measures the emit phase (declaration-emit cost); `--single-threaded` uses one checker for cleaner per-file timing attribution at the cost of wall-clock speed.

`perf summary` on the fixture:

```
$ tsagent perf summary
Files 71  Lines 56311  Symbols 58501  Types 39384  Instantiations 45549  Mem 142107K
parse 0.012s (9%)  bind 0.004s (3%)  check 0.123s (88%)  emit 0.000s (0%)  total 0.139s
types/file 554.7  instantiations/type 1.16  depth-limit hits 0
verdict: check-dominated — the type system is the bottleneck
```

### `report` — multi-page HTML reports

The `report` family renders **self-contained, multi-page HTML** dashboards: one file with all CSS/JS inlined (no network, no external assets), a sidebar that switches between pages, and deep links via `#pageid`. A report is composed of *items*; each item contributes one or more pages. The output is **measurement, not linting** — rankings, distributions, and heatmaps that quantify the codebase, never pass/fail verdicts.

| Command | Description |
|---|---|
| `report perf` | The performance report (type-system budget, hot files/types/checks, file-time treemap) — the HTML home of the `perf` data |
| `report quality` | Semantic analyses: duplicates, complexity, assertions, exhaustiveness, barrel cost, side effects, dependencies (the cheap, curated set) |
| `report structure` | File/AST size & shape: largest files (+ LOC treemap), comment density (+ heatmap), function metrics (length / nesting / params), per-directory rollups |
| `report full` | Every group at once (perf + quality + structure) |
| `report --include a,b,c` | An explicit set of items, e.g. `--include perf,duplicates,file-size` |

Shared flags: `--out <path>` (default `tsagent-report.html`) · `--top N` (rows per ranking table, default 25) · perf-capture knobs `--emit` / `--single-threaded` / `--include-libs` (forwarded to the perf item). The two expensive analyses — `dead-code` and `churn-risk` (project-wide find-all-references; churn also shells to git) — are **opt-in**: add `--include-expensive` to a preset, or name them in `--include`. Unknown `--include` items exit 2 with the list of known items. Per-item failures are non-fatal: the report is still written with the pages that succeeded and the command exits 5 (partial).

Item keys for `--include`: `perf`, `duplicates`, `complexity`, `assertions`, `exhaustiveness`, `barrel-cost`, `side-effects`, `unused-deps`, `dead-code`, `churn-risk`, `file-size`, `comments`, `functions`, `dir-stats`.

```
$ tsagent report structure --out structure.html
Structure    File size  (8)
Structure    Comment density  (8)
Structure    Function metrics  (16)
Structure    Directories  (1)
wrote HTML report: structure.html (4 page(s))
```

The shared rendering toolkit lives in `internal/tsagent/report` (the `Document`/`Page`/`Section` model and widgets — KPIs, phase bars, ranking tables with meter bars, treemap heatmaps); items map analysis data onto it in `internal/tsagent/cmds/report_*.go`.

### `refactor` — transactional mutations

All refactor commands share the transaction flags `--apply`, `--allow-errors`, and `--strict-gate` (see Transactions below). Targeted commands (`rename`, `safe-delete`, `inline`, `mv-symbol`, `signature`) share `--at file:line:col`, `--symbol <id>`, `--name <n>`, `--kind <k>` (or pass the target as the first positional argument).

| Command | Description | Extra flags |
|---|---|---|
| `refactor rename <target> <new-name>` | Rename a symbol across the project (imports, strings in import paths, etc.) | — |
| `refactor mv <from…> <to>` | Move files with all import specifiers updated (`<to>` may be a directory) | — |
| `refactor organize-imports [path…]` | Sort, merge, and remove unused imports | — |
| `refactor safe-delete <target>` | Delete a symbol only if nothing references it | `--cascade` (also delete unexported top-level symbols that become dead as a result, recursively; the cascade tree is reported in the notes) |
| `refactor apply-edits [file\|-]` | Apply a machine-generated edit set (JSON, same shape as `check --with-edits`) or a unified diff as one transaction | `--diff` (input is a unified diff instead of edits JSON) |
| `refactor exports --to named\|default <file…>` | Convert default ↔ named exports with all import sites updated | `--to named\|default` (required) |
| `refactor mv-symbol <target> --to <file>` | Move a top-level declaration to another file, fixing imports and `export { X } from` re-exports both ways | `--to <file>` (required), `--create` (create the destination file), `--with-deps` (move file-local unexported declarations the symbol transitively references along with it) |
| `refactor inline <target>` | Inline a const variable or simple (single-return) function at all usage sites and delete the declaration | — |
| `refactor extract --range <r> --into constant\|function --name <n>` | Extract an expression to a constant or statements to a function | `--range file:startLine:startCol-endLine:endCol` (1-based, end-exclusive column), `--into`, `--name` |
| `refactor signature <target> --ops <json>` | Change a function signature (add/remove/reorder params), updating all call sites | `--ops '[{"op":"add","name":"x","type":"number","default":"1","index":0},{"op":"remove","name":"x"},{"op":"reorder","order":[1,0]}]'`, `--fill-with <expr>` (call-site value for new params without a default), `--force` (remove params even when used in the body) |

`refactor extract` dry-run against the fixture (note how the diff is the answer):

```
$ tsagent refactor extract --range src/shapes.ts:37:20-37:47 --into constant --name circleArea
--- a/src/shapes.ts
+++ b/src/shapes.ts
@@ -34,7 +34,8 @@
 export function area(shape: Shape): number {
     switch (shape.kind) {
         case "circle":
-            return Math.PI * shape.radius ** 2;
+            const circleArea = Math.PI * shape.radius ** 2;
+            return circleArea;
         case "square":
             return shape.side ** 2;
         case "triangle":
dry-run: 1 file(s) would change (pass --apply to write)
```

### `context` — LLM context curation

| Command | Description | Flags |
|---|---|---|
| `context pack <target…>` | Budgeted, prioritized source slices for symbols: declaration + type dependencies (one hop) + usage examples from other files | `--symbol`, `--name`, `--token-budget N` (default 4000; tokens ≈ bytes/4), `--usage-examples N` (default 2), `--for edit\|review\|explain` (tunes slice ordering) |
| `context expand <file:line[:col]>` | Expand a position to its semantic enclosure (or ±N lines) plus the types used inside | `--radius semantic\|N` (default `semantic`), `--token-budget N` (default 2000) |
| `context delta --since <git-ref>` | Changed symbols since a git ref plus their blast radius (dependent files), as ready-to-paste review context | `--since <ref>` (required), `--max-dependents N` (default 5) |

`context pack` against the fixture — declaration first, then the types it needs, then a real usage:

```
$ tsagent context pack describeShape
=== src/shapes.ts (lines 23-31) ===
export function describeShape(shape: Shape): string {
    switch (shape.kind) {
        ...
    return "unknown shape";
}

=== src/shapes.ts (lines 20-20) ===
export type Shape = Circle | Square | Triangle;

=== src/index.ts (lines 15-23) ===
    const shape: Shape = { kind: "circle", radius: counts.get() };
    return [
        ...
        describeShape(shape),
        ...
}

~132 estimated tokens (budget 4000)
```

### `analyze` — quality & analysis

| Command | Description | Flags |
|---|---|---|
| `analyze dead-code [path…]` | Unreferenced symbols, confidence-classified (`certain` / `dynamic-risk`) | `--include-exports`, `--entry <files>` (comma-separated live roots), `--kind …`, `--fix-plan` |
| `analyze assertions [path…]` | Inventory of `as` casts, `!` assertions, `satisfies`, ts-ignore directives | — |
| `analyze unused-deps` | package.json dependencies vs. actual imports (unused + phantom) | `--dev` (include devDependencies; default true) |
| `analyze complexity [path…]` | Cyclomatic + cognitive + type complexity hotspots per function | `--top N` (default 25) |
| `analyze exhaustiveness [path…]` | Switches over literal unions/enums that miss variants | `--strict` (report even with a `default` clause) |
| `analyze duplicates [path…]` | AST-based structural clone detection (exact-structure clone classes; identifiers/literals normalized) | `--min-nodes N` (default 25), `--cross-file-only` |
| `analyze side-effects <target…>` | Purity verdict per function (transitive, following calls): targets are `file:line:col`, symbol IDs, or names | `--module [path…]` (classify top-level side effects of files instead), `--depth N` (default 3 call levels in function mode) |
| `analyze barrel-cost` | Barrel files, their transitive import cost, and direct-import rewrites | `--min-reexports N` (default 3), `--fix-plan` (emit an edit set replacing barrel imports with direct imports) |
| `analyze churn-risk` | High-churn × high-reference symbols (requires git history) | `--since <window>` (default `"6 months ago"`), `--top N` (default 25) |

`analyze duplicates` against the planted clone pair in the fixture:

```
$ tsagent analyze duplicates
clone class 1: 2 members, 36 nodes (exact-structure)
  src/dup.ts:4-12  sumPositiveSquares
  src/dup.ts:14-22  sumPositiveWeights
total: 1 clone class(es)
```

### `diagram` — diagram generation

| Command | Description | Flags |
|---|---|---|
| `diagram deps [path…]` | Module dependency diagram, cycles highlighted | `--out mermaid\|dot\|json`, `--cluster-by dir`, `--externals`, `--cycles-only`, `--neighbors` (default true) |
| `diagram classes [path…]` | Class/interface diagram: inheritance, implementation, composition | `--out …`, `--symbol <id>` + `--depth N`, `--members signatures\|names\|none`, `--include-aliases`, `--composition`, `--collapse-external` (default true) |
| `diagram calls <target>` | Call-graph diagram from a root symbol | `--out …`, `--symbol`, `--name`, `--direction in\|out\|both` (default `out`), `--depth N` (default 3) |
| `diagram flow <target>` | Control-flow graph of one function (structural CFG) | `--out mermaid\|dot\|json`, `--symbol`, `--name` |
| `diagram state <type-target>` | State machine from a discriminated union: states are the discriminant literals, transitions are project functions whose first parameter and return type relate to the union | `--out mermaid\|dot\|json`, `--symbol`, `--name`, `--discriminant <prop>` (default: auto-detected) |

`diagram state` over the fixture's `Shape` union and its transition functions:

```
$ tsagent diagram state --name Shape
stateDiagram-v2
  circle: circle
  square: square
  triangle: triangle
  circle --> square: squareUp
  square --> circle: roll
  square --> square: squareUp
  triangle --> square: squareUp
```

### `check` — diagnostics & speculative edits

| Command | Description | Flags |
|---|---|---|
| `check [path…]` | Batch diagnostics with filters | `--suggestions`, `--severity error,warning,…`, `--code TS2322,…`, `--path-glob <glob>`, `--with-diff <patch>`, `--with-edits <json>`, `--fail-on-regression` |
| `check fix <diagRef…>` or `check fix --code TSnnnn` | Apply language-service code fixes for diagnostics, as a transaction (dry-run by default; `--apply`/`--allow-errors` as for refactors) | `--code TS2322,…` (fix every fixable diagnostic with these codes; mutually exclusive with diagRef args), `--fix-id <text>` (pick among several fixes by title substring; default first) |
| `check watch` | Stream diagnostics deltas as ndjson while files change (poll-based) | `--interval <dur>` (default `2s`), `--max-rebuilds N` (0 = run until SIGINT/SIGTERM); always emits ndjson, `--format` is ignored |

Path globs use tsconfig include syntax (e.g. `src/**/*`, `src/*.ts`).

**What `check fix` can actually fix in one-shot mode.** The language service registers three quickfix families, and one-shot runs have no auto-import index (it is built only inside an editor/LSP session), so in practice:

- **Isolated-declarations annotation fixes work fully** — the `isolatedDeclarations` TS90xx diagnostics (add explicit return/parameter/property type annotations, `satisfies … as …`); these apply and pass the gate.
- **Missing-import fixes** (the TS2304 "Cannot find name" family) and the **class-implements member synthesizer** (TS2420/TS2720 — its generated members need the import adder) report `nofix … fix requires the auto-import index (available only in an editor/LSP session)`.
- **Everything else has no registered code fix** — e.g. TS6133 (unused variable) reports `nofix … no code fix available`.

A run where nothing is fixable exits 1 (`no fixable diagnostics`); exit 5 (partial) is reachable only when fixable isolated-declarations diagnostics and unfixable ones mix in one run.

### `api` — public API surface

| Command | Description | Flags |
|---|---|---|
| `api surface [entry…]` | Public API surface as a stable, sorted report with a content digest | — |
| `api diff --base <git-ref>` | Breaking-change classification between two API surfaces | `--base` (required), `--head` (default: working tree), `--fail-on breaking\|possibly-breaking` |
| `api docs [entry…]` | Structured docs (JSDoc summary + tags + resolved types) per exported symbol | `--symbol <id>` / `--name <n>` (document one symbol instead of entry files) |

### `serve` — session daemon

| Command | Description | Flags |
|---|---|---|
| `serve` | Start the daemon (ndjson JSON-RPC) | `--stdio`, `--socket` (default mode; socket path printed to stdout), `--socket-path <path>` |
| `serve status` | Query a running daemon (memory, program size, overlays, rebuilds) | `--socket-path` |
| `serve stop` | Shut down a running daemon | `--socket-path` |
| `serve reload` | Force a running daemon to rebuild its program | `--socket-path` |
| `serve overlay set <file> \| drop <file…> \| list` | Manage overlays on a running daemon (`set` reads content from `--from <path>` or stdin) | `--socket-path`, `--from <path>` |
| `serve snapshot save\|restore\|drop <name> \| list` | Manage named overlay snapshots on a running daemon | `--socket-path` |

## Transactions: dry-run by default

Every `refactor` command (and `check fix`) follows the same contract (**exception:** `tsagent edit` applies by default and takes `--dry-run` instead of `--apply` — see its section above; the diagnostics gate is identical):

1. **Dry-run is the default.** The command computes the full edit set and prints unified diffs plus `dry-run: N file(s) would change (pass --apply to write)`. Nothing is written.
2. **`--apply` writes atomically.** Before writing, tsagent rebuilds the program in memory with the edits applied and compares diagnostics. If **new** errors would appear, the apply is **refused** (exit 4), the new errors are listed, and the disk is untouched. Pre-existing errors elsewhere in the project do not block — only the *delta* matters.
3. **Unused-symbol diagnostics do not gate by default.** New TS6133/TS6192/TS6196/TS6198/TS6199 ("declared but never read/used") diagnostics — the ones `noUnusedLocals`/`noUnusedParameters` produce when you insert a helper *before* wiring it up — are kept out of `newErrors`, reported separately as `unusedWarnings`, and announced with `note: N unused-symbol diagnostic(s) introduced (not gating; --strict-gate to gate on them)`. Pass **`--strict-gate`** (available on `edit`, every `refactor` command, and `check fix`) to refuse on them too.
4. **`--allow-errors` bypasses the gate** when you know what you're doing.
5. The result reports the diagnostics delta (`newErrors`, `unusedWarnings`, `fixedErrors`) so an agent immediately knows the consequences of its edit.

On very large programs (> 2000 files) the gate — and the speculative `check --with-diff`/`--with-edits` below — announces the rebuild with a one-line stderr note (`tsagent: type-checking project (16015 files)…`), so a multi-minute type-check never reads as a hang; stdout stays machine-clean.

## Speculative checks: `--with-diff` / `--with-edits`

`check` can answer "what would the diagnostics be **if** I applied this change?" without touching disk. The patch is applied to an in-memory overlay file system, a fresh program is built, and the diagnostics deltas are classified as `new` / `fixed` / `moved` / `unchanged`.

Worked example — fix the planted error in the fixture and verify *before* editing:

```
$ cat fix.patch
--- a/src/broken.ts
+++ b/src/broken.ts
@@ -1,4 +1,4 @@
 // THIS FILE CONTAINS A DELIBERATE TYPE ERROR.
 // `check` must report TS2322 here; everything else in the fixture is clean.
 
-export const oops: number = "not a number";
+export const oops: number = 42;

$ tsagent check --with-diff fix.patch
fixed src/broken.ts:4:14 error TS2322: Type 'string' is not assignable to type 'number'.
speculative check: 0 new, 1 fixed, 0 moved, 0 unchanged
```

Use `-` to read the patch from stdin. `--fail-on-regression` makes the command exit 1 when any `new` errors appear — ideal as an agent's pre-apply gate. `--with-edits` takes structured edits instead of a diff: `{"edits":[{"file":"src/a.ts","edits":[{"pos":0,"end":3,"newText":"x"}]}],"ops":[{"kind":"create|delete|rename","path":"…","newPath":"…","content":"…"}]}` with byte-offset `pos`/`end`.

## The daemon: `tsagent serve`

One-shot invocations rebuild the program every time (fine for small projects; ~hundreds of ms here, seconds on big codebases). The daemon keeps a Workspace warm and rebuilds incrementally on file mtime changes.

Protocol: JSON-RPC 2.0 over newline-delimited JSON, one request/response per line, over `--stdio` or a unix socket (default path `$TMPDIR/tsagent-<hash(tsconfig)>.sock`). Registry commands are methods named `<family>/<name>` (`map/search`, `refactor/rename`, … single-command families are just `check`); params are `{"flags": {…by flag name…}, "args": […]}`. Results use the same JSON envelope as `--format json`.

```
$ tsagent serve --stdio
→ {"jsonrpc":"2.0","id":1,"method":"map/search","params":{"args":["Dog"]}}
← {"jsonrpc":"2.0","id":1,"result":{"schemaVersion":1,"total":1,"offset":0,"count":1,
   "truncated":false,"items":[{"name":"Dog","kind":"class","symbolId":"src/models.ts#Dog",
   "exported":true,"file":"src/models.ts","line":9,"col":14}]}}
→ {"jsonrpc":"2.0","id":2,"method":"session/shutdown"}
← {"jsonrpc":"2.0","id":2,"result":{"schemaVersion":1,"result":{"ok":true}}}
```

Admin methods:

- `session/status` — uptime, program file count, overlay count, rebuild count, heap stats.
- `session/overlays/set` `{"file": "src/a.ts", "content": "…"}` — shadow a file with unsaved content (subsequent queries see it).
- `session/overlays/drop` `{"files": ["src/a.ts"]}` / `session/overlays/list`.
- `session/snapshot/save|restore|drop` `{"name": "…"}` / `session/snapshot/list` — named snapshots of the current overlay set (see below).
- `session/reload` — force a full program rebuild.
- `session/shutdown` — stop the daemon.

Errors map the CLI exit codes onto JSON-RPC codes (`-32602` invalid params, `-32001` not found, `-32002` refused, `-32003` partial). Safety: **every mutating invocation** — `refactor … --apply`, `edit` (which applies by default; `--dry-run` stays allowed), `check fix --apply` — is refused over RPC while session overlays are present: the edits would be computed against the overlay content and then flushed over the disk file. Drop overlays first (`session/overlays/drop`). The guard is a single routing-layer predicate keyed on the shared `--apply`/`--dry-run` transaction-flag conventions, so future mutating commands are covered automatically; the policy text is reported by `session/status` as `mutationPolicy`. `tsagent serve status|stop|reload|overlay|snapshot` are thin one-shot clients for the socket; their replies render as compact `key: value` text by default (`--raw` for JSON), like every other command.

### `--connect`: routing one-shot commands through the daemon

Every non-`serve` command takes the global flag `--connect never|auto|require`:

- `never` (default) — build a fresh local program, ignore any daemon.
- `auto` — if the project's daemon socket exists, run the command on the daemon (warm program, overlays visible); if the daemon is unreachable, print a note to stderr and fall back to a local run.
- `require` — fail (exit 1) if the daemon cannot be reached.

The global `--socket-path <path>` flag points routing at a daemon started with a custom `serve --socket-path`; without it the per-project default path is derived from the resolved tsconfig. The daemon renders text/ndjson through the same output layer, so routed output is byte-identical to a local run, exit codes included. The `serve` family itself always runs locally, and streaming commands (`check watch`) are not suitable for routing.

### Overlay snapshots

`serve snapshot save <name>` captures the daemon's current overlay set under a name; `restore <name>` replaces the live overlays with the snapshot's (triggering a rebuild); `drop <name>` discards it. Snapshots let an agent checkpoint a speculative editing session — try an approach in overlays, snapshot it, try another, and restore the better one. Snapshots live in daemon memory only; they are lost when the daemon exits.

## Agent integration tips

- **Default text output is the token-efficient form.** Prefer it for LLM consumption; switch to `--raw` only when you need to post-process fields.
- **Chain by symbol ID.** `map search <name>` → copy the `path#name` ID → `nav refs --symbol <id>` / `type at --symbol <id>` / `refactor rename --symbol <id> NewName`. IDs are project-root-relative and stable across invocations and cwd changes.
- **Gate your edits.** Pipe your candidate patch through `check --with-diff - --fail-on-regression` before writing files; or just let `refactor … --apply` refuse on regressions (exit 4) and read the listed new errors.
- **Page big results.** `--limit`/`--offset` work on every list result; truncation is always announced.
- **Probe before deleting.** `analyze dead-code` → `refactor safe-delete --name <sym>` (it refuses if anything still references the symbol; add `--cascade` to also sweep unexported symbols that become dead) → `refactor organize-imports` to clean up imports that became unused.
- **Pack context, don't paste files.** `context pack <symbol> --token-budget N` gives a budgeted declaration + type deps + usage slice; `context delta --since <ref>` summarizes a branch for review.
- **Use exit codes.** 3 means "your target doesn't exist" (typo'd name), 4 means "refused, would break the build", 2 means "you called it wrong".

## Limitations

All ten families of the spec (`docs/agent-cli-spec.md`) are implemented. What follows is the honest per-command list of where v1 stops short.

**General**

- **One tsconfig at a time.** The workspace is a single parsed project; project references are not fanned out.
- The diagnostics gate and speculative checks build a second full program in memory — on very large projects expect `--apply`/`--with-diff` to cost roughly one extra type-check.
- **Named class/function expressions are first-class scopes** (`map outline --symbol`, symbol IDs, `nav refs` all see `const f = () => class Name {…}` and its members); *anonymous* function-likes and class expressions remain addressable only through the `path@bytePos` fallback.

**refactor**

- **`apply-edits`** takes byte offsets against the files' *current* content and does no offset remapping; `--diff` mode applies each parsed patch as a whole-file replacement.
- **`safe-delete --cascade`** sweeps unexported *top-level* symbols only (no class members), can leave now-unused import specifiers behind (run `organize-imports` after), and caps at 10 cascade rounds.
- **`exports`** refuses re-exported defaults (`export { default } from`), `export =`, and namespace exports; on `--to default`, namespace-import consumers (`import * as ns`) are noted but not rewritten.
- **`mv-symbol`** moves a single declaration only (no overloads or merged declarations); symbols that reference file-local unexported dependencies refuse unless `--with-deps` (or `with-deps` on an edit move line) moves the transitive closure of local helpers along — deps move first in source order, stay unexported, and the move refuses when a dep is also used by a declaration staying behind, or beyond 25 deps. Named, default, and namespace imports the moved block needs are re-created in the destination. Consumers using `export { X } from` re-export clauses (incl. `as` aliases and `type`-only) are retargeted to the new file, and consumers importing through such a rewritten barrel are left untouched; consumers reached through `export * from` star re-exports or namespace imports (`import * as ns`) still refuse (the star refusal names the re-exporting file and line). It does not move default exports. When the move empties the source file it is deleted (with a note); re-exports pointing at the deleted file are retargeted before deletion. `--create` refuses destinations outside the project root.

  **Import synthesis & merging.** Every import the planner synthesizes — destination dep imports, the source's back-import, consumer retargets — first MERGES into an existing import clause from the same module: names already imported there are deduped, missing named specifiers are appended at the end of the clause, and a default-only import is extended (`import d from "m"` → `import d, { x } from "m"`). Synthesized bindings are classified type-only via the checker (interfaces, type aliases — anything without a value meaning — plus bindings that were type-only at their origin): type names prefer an existing `import type { … }` clause, otherwise they join a value clause with an inline `type` modifier; value names never enter a type-only clause (they get a value import of their own). Namespace imports (`import * as ns`) and `import type d from "m"` defaults are never merged into. Whatever cannot merge becomes ONE new statement per module — `import type { … }` when every name is type-only, inline `type` modifiers when mixed — prepended below any shebang/directive prologue, matching the file's quote style, with one blank line between a new import block and a following non-import statement.

  **Consumer specifier style (package boundaries).** A consumer whose original import (or `export { X } from` re-export) used a bare package specifier — a package.json `exports` subpath like `@scope/pkg/promise` — is retargeted to the *destination's* package specifier instead of a deep relative path that would escape the consumer's own workspace package. The planner walks up from the destination file to the nearest `package.json` with a `name` and scans its `exports` for a subpath mapping to the destination file: string targets, conditional objects (any of the condition keys `default`/`import`/`require`/`types`/`bun` counts; nested conditions and array fallbacks included), and single-`*` wildcard patterns on subpath and target (`"./*": "./src/*.ts"`) are matched; a bare-string `exports`, the `"."` subpath, or — absent an `exports` field — a `main`/`module` field naming the destination yield the bare package name. Consumers *inside* the destination's package keep relative specifiers (correct and conventional there), and consumers whose original specifier was relative keep today's relative computation. When the original specifier was bare but no exports subpath maps to the destination file, the relative path is used anyway and the result carries `note: rewrote "X" consumers with a relative path that crosses the package boundary of <pkg> (no exports subpath maps to <dest>)`.

  **Exported-via-clause symbols.** A trailing `export { helper }` (incl. `export { helper as h }`) counts as exported: such a dependency stays behind and the destination imports it back under its *exported* name (`import { h as helper } from …`). When the moved declaration itself is exported via a clause, its specifier is removed (the whole statement when it empties), the declaration gets an `export` modifier in the destination, and consumers retarget normally; only an *aliased* clause export of the moved symbol itself (`export { moveMe as renamed }`) refuses, since the move would otherwise change its public name.
- **`inline`** handles const variables and single-return functions; its side-effect analysis for arguments is conservative; it is all-or-nothing (one un-inlinable site refuses the whole transaction).
- **`extract`** supports `--into constant|function` only (no type extraction) and refuses ranges containing `return`, producing more than one output value, or writing to outer locals.
- **`signature`** does add/remove/reorder only (no parameter retyping), refuses call sites that use spread arguments, and leaves non-call references (e.g. the function passed as a callback) untouched — the diagnostics gate is the backstop there.
- **`mv` moves files, not directories** (move the contained files individually).

**nav**

- **`types --structural`** matches direct implementers at depth 1 only; an empty interface structurally matches everything; classes nested inside other declarations are not scanned.
- **`shape`** matches on textually normalized type display strings (case-sensitive), not assignability; optional parameters count as declared.
- **`export-graph`** does not track `export default`/`export =` re-export forms; barrel resolution uses an `index.*` heuristic and does not consult `package.json` `main`.

**type**

- **`explain-error`**'s drill-down is an approximate property-wise decomposition, not the checker's native elaboration chain (the chain itself is the checker's).
- **`flow`** traces within the declaring file only. (The reported types *are* flow-narrowed — `GetTypeAtLocation` does the real thing.)
- **`infer`** skips rest/binding-pattern/initializer parameters; usage-based union suggestions cap at 4 distinct types.
- **`instantiations`** is best-effort: derived from find-all-references, so only explicit type-argument references and resolved call/new sites are counted.
- **`assignable`** accepts targets only (positions / symbol IDs), not arbitrary type expressions; its drill-down is the same approximation as `explain-error`'s.

**context**

- **`pack`** collects type dependencies one hop out, not transitively; token counts are the bytes/4 estimate everywhere.
- **`delta`** uses `git diff` semantics, so brand-new untracked files are invisible; change attribution is at top-level-declaration granularity.

**analyze**

- **`duplicates`** finds exact-structure (type-2) clones only — no near-miss detection.
- **`side-effects`** uses a small name-based denylist of known-impure builtins; method calls on object values classify as `unknown`; no dynamic-dispatch resolution.
- **`barrel-cost`** offers no rewrite suggestion for default/namespace imports of a barrel, and its `--fix-plan` rewrites do not preserve `type`-only specifiers.
- **`churn-risk`** attributes file-level git churn to the symbols in the file, and needs git history to say anything.
- **`dead-code` is v1-tiered:** per-file locals and unexported module members (confidence `certain`, downgraded to `dynamic-risk` under dynamic access patterns); exported symbols only with `--include-exports`/`--entry`.

**check**

- **`fix`** has exactly one fix family that works end-to-end in one-shot mode: the isolated-declarations annotation fixes (TS90xx). Missing-import fixes (TS2304 family) *and* the class-implements fix (TS2420/TS2720 — its member synthesis needs the import adder) require the LSP auto-import index and report `nofix … fix requires the auto-import index`; every other code (TS6133, …) has no registered code fix at all (`nofix … no code fix available`). Unfixable diagnostics are reported, never silently skipped. `--fix-id` matches against the fix action's title.
- **`watch`** always emits ndjson (ignores `--format`) and is poll-based (default 2s), not fsevents/inotify.

**diagram**

- **`flow`** does not model `switch` fallthrough between case bodies, and draws a single exception edge per `try`.
- **`state`** scans direct function signatures only — top-level function declarations and function-valued consts; no methods, no `Promise` unwrapping.

**api**

- **`diff` compares type display strings** (v1), so a purely cosmetic change in how a type renders can be classified as possibly-breaking.

**serve**

- Snapshots are held in daemon memory and lost on exit.
- `--connect` defaults to `never`; streaming commands (`check watch`) are not suitable for routing. A stale socket under `--connect auto` costs one failed dial and a stderr note before falling back to a local run.
