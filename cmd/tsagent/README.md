# tsagent — TypeScript language service CLI for agents

`tsagent` is an agent-oriented command-line interface to this repository's native TypeScript compiler and language service. It answers the questions coding agents actually ask — "what is in this project?", "who calls this?", "what type is this?", "is it safe to rename/delete this?", "what breaks if I apply this diff?" — as single shell commands with compact, LLM-optimized text output (JSON on demand). Mutating commands are transactional: they dry-run by default, print a unified diff, and refuse to apply edits that would introduce new type errors. A session daemon (`tsagent serve`) keeps the program warm for repeated queries on large projects.

- Feature specification: [`docs/agent-cli-spec.md`](../../docs/agent-cli-spec.md)
- Architecture / implementation plan: [`docs/agent-cli-implementation-plan.md`](../../docs/agent-cli-implementation-plan.md)

## Install

From the repository root:

```sh
go build -o ~/.local/bin/tsagent ./cmd/tsagent
```

(Any directory on your `PATH` works. The binary embeds the TypeScript lib `.d.ts` files; it is fully self-contained.)

## Quickstart

All examples below are real output against the test fixture project (`testdata/tsagent/fixture`, a small strict-mode project with a planted type error, an import cycle, and a non-exhaustive switch). Run `tsagent` from anywhere inside a project — the nearest `tsconfig.json` is discovered automatically, or pass `--project <tsconfig|dir>`.

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
2. **Symbol ID** — `path#qualified.name`, e.g. `src/models.ts#Container.get`. Symbol IDs are printed by `map search`, `map outline --raw`, `nav calls`, and others; paths are always relative to the project root (the tsconfig directory), so IDs are stable regardless of your cwd. Positions inside a file that have no named symbol use the fallback form `path@bytePos`.
3. **Name** — `--name <identifier>` resolves a declaration by name project-wide; it must be unambiguous (exits 3 when there are no matches, errors when there are several — disambiguate with `--kind` where supported, or use a symbol ID).

Refactor commands also accept the target as the first positional argument and guess the form (`file:line:col` → position, contains `#`/`@` → symbol ID, otherwise name).

## Command reference

Global flags (valid on every command, anywhere after `<family> <command>`):
`--project <tsconfig|dir>` · `--raw` · `--format json|text|ndjson` · `--limit N` · `--offset N`

### `map` — orientation

| Command | Description | Flags |
|---|---|---|
| `map outline <path…>` | Symbol tree per file/folder with ranges and signatures | `--depth N\|top-level\|all` (default `all`), `--exported-only`, `--kind class,interface,function,…` |
| `map search <query>` | Project-wide fuzzy symbol search returning symbol IDs | `--kind …`, `--exported-only`, `--path-glob <glob>` |
| `map files` | Program file inventory with classification (source/lib/declaration) | — |
| `map stats` | Per-directory counts of files, lines, symbols, exports | — |

### `nav` — navigation & graph queries

| Command | Description | Flags |
|---|---|---|
| `nav def <target…>` | Go to definition (batch) | `--symbol`, `--name`, `--implementations`, `--type-definition` |
| `nav refs <target>` | All references with per-reference usage kinds | `--symbol`, `--name`, `--include-declaration`, `--kind declaration,import,type,call,write,read`, `--group-by file\|kind` |
| `nav usages <target>` | References grouped per file with context excerpts | `--symbol`, `--name`, `--context-lines N` (default 2) |
| `nav calls <target>` | Call hierarchy tree with cycle marking | `--symbol`, `--name`, `--direction in\|out\|both` (default `in`), `--depth N` (default 3) |
| `nav graph` | Module dependency graph with cycle detection | `--scope file\|dir`, `--cycles` (SCCs only), `--externals` |
| `nav path --from <file> --to <file>` | Shortest import chain(s) between two files | `--from`, `--to`, `--max-paths N` |

### `type` — type intelligence

| Command | Description | Flags |
|---|---|---|
| `type at <target…>` | Resolve the type at positions/symbols, structurally expanded | `--expand-depth N` (default 1), `--symbol`, `--name` |
| `type assignable --source <target> --to <target>` | Assignability check with a property-wise drill-down on failure | `--source`, `--to` |
| `type coverage [path…]` | `any`/`unknown` expressions, casts, non-null assertions, ts-ignores per file | `--threshold <pct>` (exit 1 when project any% exceeds it) |
| `type complexity [path…]` | Structural complexity of exported type aliases/interfaces/classes | `--symbol`, `--threshold N`, `--rank` (default true), `--top N` (default 50) |
| `type instantiations <generic-target>` | Best-effort list of concrete instantiations of a generic (via find-references) | — |

### `refactor` — transactional mutations

All refactor commands share the transaction flags `--apply` and `--allow-errors` (see Transactions below). Targeted commands (`rename`, `safe-delete`) share `--at file:line:col`, `--symbol <id>`, `--name <n>`, `--kind <k>`.

| Command | Description | Extra flags |
|---|---|---|
| `refactor rename <target> <new-name>` | Rename a symbol across the project (imports, strings in import paths, etc.) | — |
| `refactor mv <from…> <to>` | Move files with all import specifiers updated (`<to>` may be a directory) | — |
| `refactor organize-imports [path…]` | Sort, merge, and remove unused imports | — |
| `refactor safe-delete <target>` | Delete a symbol only if nothing references it | `--cascade` (declared, **not implemented** — exits 2) |

### `analyze` — quality & analysis

| Command | Description | Flags |
|---|---|---|
| `analyze dead-code [path…]` | Unreferenced symbols, confidence-classified (`certain` / `dynamic-risk`) | `--include-exports`, `--entry <files>` (comma-separated live roots), `--kind …`, `--fix-plan` |
| `analyze assertions [path…]` | Inventory of `as` casts, `!` assertions, `satisfies`, ts-ignore directives | — |
| `analyze unused-deps` | package.json dependencies vs. actual imports (unused + phantom) | `--dev` (include devDependencies; default true) |
| `analyze complexity [path…]` | Cyclomatic + cognitive + type complexity hotspots per function | `--top N` (default 25) |
| `analyze exhaustiveness [path…]` | Switches over literal unions/enums that miss variants | `--strict` (report even with a `default` clause) |

### `diagram` — diagram generation

| Command | Description | Flags |
|---|---|---|
| `diagram deps [path…]` | Module dependency diagram, cycles highlighted | `--out mermaid\|dot\|json`, `--cluster-by dir`, `--externals`, `--cycles-only`, `--neighbors` (default true) |
| `diagram classes [path…]` | Class/interface diagram: inheritance, implementation, composition | `--out …`, `--symbol <id>` + `--depth N`, `--members signatures\|names\|none`, `--include-aliases`, `--composition`, `--collapse-external` (default true) |
| `diagram calls <target>` | Call-graph diagram from a root symbol | `--out …`, `--symbol`, `--name`, `--direction in\|out\|both` (default `out`), `--depth N` (default 3) |

### `check` — diagnostics & speculative edits

| Command | Description | Flags |
|---|---|---|
| `check [path…]` | Batch diagnostics with filters | `--suggestions`, `--severity error,warning,…`, `--code TS2322,…`, `--path-glob <glob>`, `--with-diff <patch>`, `--with-edits <json>`, `--fail-on-regression` |

Path globs use tsconfig include syntax (e.g. `src/**/*`, `src/*.ts`).

### `api` — public API surface

| Command | Description | Flags |
|---|---|---|
| `api surface [entry…]` | Public API surface as a stable, sorted report with a content digest | — |
| `api diff --base <git-ref>` | Breaking-change classification between two API surfaces | `--base` (required), `--head` (default: working tree), `--fail-on breaking\|possibly-breaking` |

### `serve` — session daemon

| Command | Description | Flags |
|---|---|---|
| `serve` | Start the daemon (ndjson JSON-RPC) | `--stdio`, `--socket` (default mode; socket path printed to stdout), `--socket-path <path>` |
| `serve status` | Query a running daemon (memory, program size, overlays, rebuilds) | `--socket-path` |
| `serve stop` | Shut down a running daemon | `--socket-path` |

## Transactions: dry-run by default

Every `refactor` command follows the same contract:

1. **Dry-run is the default.** The command computes the full edit set and prints unified diffs plus `dry-run: N file(s) would change (pass --apply to write)`. Nothing is written.
2. **`--apply` writes atomically.** Before writing, tsagent rebuilds the program in memory with the edits applied and compares diagnostics. If **new** errors would appear, the apply is **refused** (exit 4), the new errors are listed, and the disk is untouched. Pre-existing errors elsewhere in the project do not block — only the *delta* matters.
3. **`--allow-errors` bypasses the gate** when you know what you're doing.
4. The result reports the diagnostics delta (`newErrors`, `fixedErrors`) so an agent immediately knows the consequences of its edit.

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
- `session/reload` — force a full program rebuild.
- `session/shutdown` — stop the daemon.

Errors map the CLI exit codes onto JSON-RPC codes (`-32602` invalid params, `-32001` not found, `-32002` refused, `-32003` partial). Safety: `refactor --apply` is refused over RPC while overlays are present (overlays would shadow the disk writes) — drop overlays first. `tsagent serve status` / `tsagent serve stop` are thin one-shot clients for the socket.

## Agent integration tips

- **Default text output is the token-efficient form.** Prefer it for LLM consumption; switch to `--raw` only when you need to post-process fields.
- **Chain by symbol ID.** `map search <name>` → copy the `path#name` ID → `nav refs --symbol <id>` / `type at --symbol <id>` / `refactor rename --symbol <id> NewName`. IDs are project-root-relative and stable across invocations and cwd changes.
- **Gate your edits.** Pipe your candidate patch through `check --with-diff - --fail-on-regression` before writing files; or just let `refactor … --apply` refuse on regressions (exit 4) and read the listed new errors.
- **Page big results.** `--limit`/`--offset` work on every list result; truncation is always announced.
- **Probe before deleting.** `analyze dead-code` → `refactor safe-delete --name <sym>` (it refuses if anything still references the symbol) → `refactor organize-imports` to clean up imports that became unused.
- **Use exit codes.** 3 means "your target doesn't exist" (typo'd name), 4 means "refused, would break the build", 2 means "you called it wrong".

## Limitations

Honest list of where the implementation currently stops short of the spec (`docs/agent-cli-spec.md`):

- **Spec'd commands not yet implemented:** `nav types|shape|export-graph`; `type explain-error|infer|flow`; `refactor mv-symbol|extract|inline|signature|exports|modules|apply-edits`; `analyze duplicates|side-effects|churn-risk|barrel-cost`; `diagram flow|state`; `check fix|watch`; the entire `context` family (`pack|expand|delta`); `api docs`; `serve snapshot save|restore` (snapshots are not implemented).
- **No CLI auto-connect / `--connect` routing.** One-shot commands always build a fresh program; they do not transparently route to a running daemon. The daemon is reachable only via its RPC protocol (or `serve status`/`serve stop`). Overlay management likewise has no CLI subcommands — RPC `session/overlays/*` only.
- **`refactor safe-delete --cascade`** is declared but not implemented (exits 2); deleting a symbol does not cascade to symbols that become dead.
- **`type instantiations` is best-effort:** derived from find-all-references, so only explicit type-argument references and resolved call/new sites are counted; inferred or indirect instantiations can be missed.
- **`type assignable` accepts targets only** (positions / symbol IDs), not arbitrary type expressions; its failure drill-down is an approximate property-wise comparison, not the checker's native elaboration chain.
- **`analyze dead-code` is v1-tiered:** it covers per-file locals and unexported module members (confidence `certain`, downgraded to `dynamic-risk` under dynamic access patterns). Exported symbols are only analyzed with `--include-exports`/`--entry`.
- **`api diff` compares type display strings** (v1), so a purely cosmetic change in how a type renders can be classified as possibly-breaking.
- **`refactor mv` moves files, not directories** (move the contained files individually).
- **One tsconfig at a time.** The workspace is a single parsed project; project references are not fanned out.
- The diagnostics gate and speculative checks build a second full program in memory — on very large projects expect `--apply`/`--with-diff` to cost roughly one extra type-check.
