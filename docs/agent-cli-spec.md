# tsagent — TypeScript Language Service CLI for Agents

**Status:** Draft specification
**Base:** Fork of [typescript-go](https://github.com/microsoft/typescript-go) (`internal/ls`, `internal/checker`, `internal/compiler`)

> **Implementation status:** this spec is implemented end-to-end at `cmd/tsagent` /
> `internal/tsagent` — all ten command families (`map`, `nav`, `type`, `refactor`,
> `analyze`, `diagram`, `check`, `context`, `api`, `serve`), 60 commands in total.
> The [user manual](../cmd/tsagent/README.md) is the authoritative reference for
> the commands and flags as shipped; the remaining per-command gaps are listed in
> its Limitations section. See the
> [implementation plan](./agent-cli-implementation-plan.md) for the architecture.
> Where this document and the README disagree, the README describes shipped
> behavior; this document remains the target design.

---

## 1. Vision

LSP is shaped for human editors: positions are cursor offsets, results are UI affordances (hovers, code lenses, squiggles), and the protocol assumes a long-lived editor session with a human deciding each next step.

Agents need something different:

- **Batch-oriented**: resolve 50 positions in one call, not 50 round trips.
- **Structured & stable**: JSON output with durable symbol IDs that can be chained between calls ("find usages → pick #3 → rename").
- **Token-efficient**: compact text formats and budget-aware output for stuffing LLM context.
- **Speculative**: "what would happen if I applied this edit?" without touching disk.
- **Transactional**: every mutation supports dry-run, returns a diff, and applies atomically.

`tsagent` is a CLI (and persistent session daemon) that exposes the TypeScript language service through this lens.

---

## 2. Design Principles

These apply to **every** command unless stated otherwise.

### 2.1 Stable symbol IDs

Every symbol-bearing result carries a `symbolId` — a stable handle valid for the lifetime of a session (and best-effort stable across sessions via `file#qualifiedName` encoding). All commands that accept a target accept any of:

| Addressing mode | Example |
|---|---|
| Position | `--at src/checker.ts:1041:17` |
| Symbol ID | `--symbol 'src/checker.ts#Checker.getTypeAtLocation'` |
| Qualified name search | `--name 'getTypeAtLocation' --kind method` |

### 2.2 Output formats

- `--format json` (default for piping): stable schema, versioned with `"schemaVersion"`.
- `--format text`: compact, indented, token-efficient — designed to be pasted into LLM context.
- `--format ndjson`: streaming, one result per line, for large result sets.
- All list-shaped output supports `--limit`, `--offset`, and emits `"truncated": true` + total counts. **No silent truncation, ever.**

### 2.3 Token budgets

Commands that emit source code or large trees accept `--token-budget N`. The command degrades gracefully (drops bodies → drops signatures → drops leaf nodes) and reports what was elided.

### 2.4 Mutations are transactions

Every mutating command (`mv`, `rename`, `refactor *`, `fix *`):

- `--dry-run` (default **on** unless `--apply` given): returns a unified diff + affected-file list, applies nothing.
- `--apply`: applies atomically — all files or none.
- Returns post-apply diagnostics delta (`newErrors`, `fixedErrors`) so the agent immediately knows if the edit broke something.
- `--allow-errors`: apply even if new diagnostics appear (default: refuse and roll back).

### 2.5 Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Operation failed (internal error, bad project) |
| 2 | Invalid arguments |
| 3 | Target not found (symbol/file/position resolves to nothing) |
| 4 | Mutation refused (would introduce errors; see 2.4) |
| 5 | Partial success (some batch items failed; per-item status in output) |

### 2.6 Project resolution

All commands take `--project <tsconfig.json|dir>` (auto-discovered from cwd by default) and `--root-dir`. Multi-project workspaces: `--project` may repeat; results are namespaced by project.

---

## 3. Architecture

```
┌─────────────────────────────────────────────────┐
│ tsagent CLI (one-shot)                          │
│   parses args → runs against fresh Program      │
└──────────────────────┬──────────────────────────┘
                       │ or
┌──────────────────────▼──────────────────────────┐
│ tsagent serve (session daemon)                  │
│   • persistent Program + checker caches         │
│   • JSON-RPC over stdio or unix socket          │
│   • overlay store (speculative edits)           │
│   • symbol ID registry                          │
│   • file watcher → incremental re-check         │
└──────────────────────┬──────────────────────────┘
                       │ reuses
┌──────────────────────▼──────────────────────────┐
│ typescript-go internals                         │
│   internal/compiler  (Program, builder)         │
│   internal/checker   (types, symbols, flow)     │
│   internal/ls        (references, rename, …)    │
│   internal/format    (printing edits)           │
└─────────────────────────────────────────────────┘
```

- **One-shot mode**: `tsagent <command>` builds the program, answers, exits. Fine for small projects and CI.
- **Session mode**: `tsagent serve` keeps the program warm. The CLI auto-connects to a running session for the same project (like `gradle --daemon`), so agents get one-shot ergonomics with session performance.
- **Overlays**: sessions hold an overlay store — in-memory file contents layered over disk. `check --with-diff` and all `--speculative` operations run against overlays. This reuses the LSP document-overlay machinery already in typescript-go.

---

## 4. Command Reference

Commands are grouped into ten families:

```
tsagent map        …   orientation & symbol trees
tsagent nav        …   navigation & graph queries
tsagent type       …   type intelligence
tsagent refactor   …   mutations & refactoring
tsagent analyze    …   quality & analysis
tsagent diagram    …   diagram generation
tsagent check      …   diagnostics & speculative edits
tsagent context    …   LLM context packing
tsagent api        …   public API surface tooling
tsagent serve      …   session daemon & admin
```

---

### 4.1 `tsagent map` — Orientation & symbol trees

The "where am I" family. First thing an agent should call in an unfamiliar repo.

#### `map outline <path…>`

Print the symbol tree per file and/or folder.

```
tsagent map outline src/ --depth top-level --exported-only --format text
tsagent map outline src/checker.ts --detail full
```

- Nested tree of declarations: classes → methods/props, namespaces, functions, types, enums, consts.
- Each entry: `kind`, `name`, `symbolId`, exported flag, visibility, one-line signature, line range.
- `--depth N | top-level | all` — folder-level aggregation with depth control. `top-level` over `src/**` is effectively a repo map.
- `--detail names|signatures|full` — `full` adds first JSDoc line and reference counts.
- `--kind class,interface,function,…` and `--exported-only`, `--include-private` filters.
- Text format is an indented outline (ctags-meets-tree), dramatically cheaper in tokens than JSON.

#### `map files`

Project file inventory as the compiler sees it.

- Root files, lib files, declaration files, node_modules entries actually included.
- `--why <file>` — explain *why* a file is in the program (the include/import chain), like `--explainFiles` but structured.

#### `map stats`

Counts per directory: files, lines, symbols by kind, export counts, `any` density. A one-screen quantitative overview.

#### `map search <query>`

Project-wide fuzzy symbol search (workspace-symbols, but batch and filterable).

- `--kind`, `--exported-only`, `--path-glob`, `--regex`.
- Returns `symbolId`s ready to chain into any other command.

---

### 4.2 `tsagent nav` — Navigation & graph queries

#### `nav def <target…>`

Go to definition / declaration. **Batch-friendly**: accepts many targets, returns one result per target.

- `--implementations` — resolve to implementations rather than declarations (interfaces → classes, overloads → bodies).
- `--type-definition` — jump to the type of the expression instead.

#### `nav refs <target>`

Find all references (the canonical "getting refs").

- `--include-declaration`, `--write-only` (assignments/mutations only), `--read-only`.
- `--group-by file|kind` — kind classifies each ref: call, type-position, import, re-export, write, read.
- Returns ranges + a `usageKind` per ref so agents can filter without fetching source.

#### `nav usages <target>`

Higher-level than `refs`: semantically grouped usages with surrounding context lines.

- `--context-lines N`, `--callers-only`, `--token-budget`.
- Distinguishes "imported but unused", "type-only usage", "runtime usage".

#### `nav calls <target>`

Call hierarchy.

- `--direction in|out|both`, `--depth N`.
- Output is a tree with cycles marked, each node a `symbolId`.

#### `nav types <target>`

Type hierarchy: supertypes/subtypes of a class or interface.

- `--direction up|down|both`, `--depth N`.
- `--structural` — include structural implementers (things that *satisfy* the interface without `implements`), checker-powered; this is the thing grep can never do.

#### `nav graph`

Module dependency graph (imports between files/packages).

- `--scope file|dir|package`, `--format json|dot|mermaid`.
- `--cycles` — only emit strongly-connected components (cycle detection).
- `--externals` — include/exclude node_modules edges.

#### `nav path --from <target> --to <target>`

Reachability query: can code in A reach symbol B? Returns the shortest call/import chain(s) between two symbols, or "unreachable".

- `--via calls|imports|both`, `--max-paths N`.

#### `nav shape <signature>`

Symbol search by type shape: find all functions matching a signature pattern.

```
tsagent nav shape '(s: string) => Promise<*>' --kind function
```

- Wildcards in param/return positions; matching is assignability-based via the checker, not textual.

#### `nav export-graph`

Symbol-level export/re-export graph: where a symbol is declared, every barrel it flows through, and the final public name(s) it is reachable as.

---

### 4.3 `tsagent type` — Type intelligence

#### `type at <target…>`

Resolve the fully-expanded type at one or more positions.

- `--expand-depth N` — control alias/mapped-type expansion (agents hate `Omit<Pick<…>>` soup; default expands one level, `--expand-depth full` flattens to structure).
- `--no-truncate` — disable the checker's display truncation.
- Output includes both the *display* string and a structural JSON form (`properties`, `callSignatures`, `unionMembers`, …).

#### `type assignable --source <type|target> --to <type|target>`

Type compatibility check without writing test code. Accepts type expressions (`--source 'Partial<Config>'`) or symbol/position targets.

- On failure, returns the elaboration chain: *which* property, *why*, recursively.

#### `type explain-error <diagnostic-ref|file:line:col>`

Take a diagnostic and decompose the assignability failure step by step — the checker's elaboration, fully expanded, as a structured causal chain. Built for agents to *act on* errors rather than pattern-match message text.

#### `type complexity <path…>`

Per-type complexity calculation (instantiation depth, union width, structural size, recursion, conditional-type nesting).

- `--threshold N` — only report types over budget.
- `--rank` — project-wide leaderboard of the most expensive types.
- `--why <symbol>` — break a single type's cost down by contributing part.
- Pairs with `--trace` to dump checker instantiation counts (the `tsc --generateTrace` data, but per-type and structured).

#### `type infer <target…>`

Infer types from usage: suggest types for untyped/implicitly-`any` params, returns, and variables based on call sites and assignments. Returns suggestions as candidate edits (chainable into `refactor apply-edits`).

#### `type coverage <path…>`

Type coverage report: % of expressions that are `any` / `unknown` / implicit-any / `as`-casted, per file and aggregate.

- `--strict-candidates` — files that would pass under stricter flags than currently configured.

#### `type instantiations <generic-target>`

List all concrete instantiations of a generic type/function across the project, with locations and the inferred type arguments.

#### `type flow <target>`

Control-flow narrowing trace for a variable: every point its type narrows/widens within a function, and why. Invaluable for "why is this still `string | undefined` here?" questions.

---

### 4.4 `tsagent refactor` — Mutations & refactoring

All commands here follow the transaction rules in §2.4 (dry-run default, atomic apply, diagnostics delta).

#### `refactor mv <from…> <to>`

Move files or folders with all imports updated (both directions: imports *of* the moved files, and imports *in* them).

- Handles path-mapped imports, `index` resolution, declaration-file pairs.

#### `refactor mv-symbol <target> --to <file>`

Move a single symbol to another file. Finer-grained than file moves: fixes imports both ways, creates the destination file if needed, carries doc comments and local helper dependencies (`--with-deps` to also move private helpers only it uses).

#### `refactor rename <target> <new-name>`

Rename a symbol across the project.

- `--include-strings` — also update matching string literals/property names where safe (e.g. `keyof` constraints, JSON-pointer style usages) — each reported as `confidence: certain|likely` so the agent can review.
- `--include-comments`, `--include-filenames` (rename file when renaming its single default export).

#### `refactor extract --range <file:start-end> --into function|constant|type [--name <n>]`

Extract function/constant/type from a span, with auto-computed parameters, return type, and async-ness.

#### `refactor inline <target>`

Inline a function or variable at all (or selected `--at`) usage sites. Refuses when side-effect ordering would change (`--force` to override, with a warning report).

#### `refactor signature <target> --ops <json>`

Change function signature: add/remove/reorder/retype parameters, update **all** call sites atomically.

```
tsagent refactor signature --symbol 'src/api.ts#fetchUser' \
  --ops '[{"op":"add","name":"opts","type":"FetchOpts","default":"{}"},{"op":"reorder","order":[1,0,2]}]'
```

- New required params at call sites get a placeholder + emitted TODO list, or a `--fill-with <expr>` strategy.

#### `refactor exports <path…> --to named|default`

Convert default ↔ named exports with all import sites updated.

#### `refactor modules <path…> --to esm|cjs`

Convert CommonJS ↔ ESM per file or directory (require/module.exports ↔ import/export, with interop edge cases reported rather than silently guessed).

#### `refactor organize-imports <path…>`

Organize/normalize imports project-wide: sort, merge, split type-only, remove unused, enforce path-alias preferences (`--prefer-alias`, `--prefer-relative`).

#### `refactor safe-delete <target…>`

Delete a symbol (or file) **only if** no references remain; otherwise fail with the blocking references. `--cascade` also deletes things that become dead as a result, recursively, with the full cascade in the dry-run diff.

#### `refactor apply-edits <edits.json>`

Apply a machine-generated edit set (the format every other command emits) as one transaction. This is the universal "commit" primitive: agents can collect edits from `type infer`, `analyze dead-code --fix-plan`, etc., review them, then apply in one shot.

---

### 4.5 `tsagent analyze` — Quality & analysis

#### `analyze dead-code <path…>`

Scan for dead code: unexported-and-unreferenced symbols, unreachable branches, unused exports (`--entry <file…>` defines the live roots; defaults to package.json entries + tests).

- `--include-exports` — treat unused *exports* as dead (off by default for libraries).
- `--fix-plan` — emit an edit set for `refactor apply-edits` (delete in dependency order).
- Classifies confidence: `certain` (no refs at all) vs `dynamic-risk` (name appears in strings/reflection).

#### `analyze complexity <path…>`

Hotspot ranking: cyclomatic + cognitive + **type** complexity (from `type complexity`) combined per function/file. `--top N`.

#### `analyze duplicates <path…>`

AST-based structural clone detection (not text diffing). Reports clone classes with normalized-diff between members, ranked by size × count.

- `--min-nodes N`, `--cross-file-only`.

#### `analyze side-effects <target…>`

Purity analysis: does this function/module mutate state, perform I/O, or import for side effects? Returns a verdict + the evidence chain (every impure operation reached, transitively, with depth).

- `--module` mode: is this file safe to tree-shake / import lazily?

#### `analyze exhaustiveness <path…>`

Find switches and if-chains over discriminated unions/enums that miss cases. Reports the missing variants per site. `--fix-plan` emits the skeleton cases.

#### `analyze unused-deps`

Cross-reference package.json dependencies against actual imports in the program: unused deps, phantom deps (imported but undeclared), type-only deps that could move to devDependencies.

#### `analyze churn-risk`

Join the symbol graph with git history: symbols with high reference counts **and** high change frequency — the "blast radius" list an agent should be careful around. (Requires git; degrades gracefully without it.)

#### `analyze barrel-cost`

Find barrel files (`index.ts` re-export hubs) and measure their cost: how many modules each barrel pulls into every importer, with suggested direct-import rewrites (`--fix-plan`).

#### `analyze assertions <path…>`

Inventory of trust boundaries: every `as` cast, non-null `!`, `any` parameter, `@ts-ignore`/`@ts-expect-error`, with the asserted-vs-actual type at each. The "where could the types be lying" map.

---

### 4.6 `tsagent diagram` — Diagram generation

All diagram commands emit `--format mermaid|dot|json` (mermaid default — pastes straight into markdown).

#### `diagram classes <path…|target…>`

Class/interface diagram: inheritance, implementation, composition (typed-property) edges.

- `--scope` to bound, `--collapse-external`, `--members signatures|names|none`.

#### `diagram deps`

Module/package dependency diagram (the renderable view of `nav graph`). `--cluster-by dir|package`.

#### `diagram calls <target>`

Call-graph diagram from a root symbol, `--depth N`, cycles highlighted.

#### `diagram flow <target>`

Control-flow graph of a single function (branches, loops, early returns), useful for explaining complex functions.

#### `diagram state <type-target>`

For discriminated unions used as state machines: derive states from the union variants and transitions from functions that take one variant and return another.

---

### 4.7 `tsagent check` — Diagnostics & speculative edits

#### `check <path…>`

Batch diagnostics with filters: `--severity`, `--code TS2345,TS2322`, `--path-glob`, `--since <git-ref>` (only diagnostics in changed regions). Stable JSON, paginated, each diagnostic carrying a `diagRef` usable with `type explain-error`.

#### `check --with-diff <patch.diff>` / `check --with-edits <edits.json>`

**Speculative edit check** — the single highest-value action for agents: "if I applied this change, what diagnostics would appear/disappear?" Runs against session overlays; disk is never touched.

- Output: `newErrors`, `fixedErrors`, `unchangedErrors` (counts + details).
- `--at-stage parse|bind|check` for fast pre-screens.

#### `check fix <diagRef…|--code TSxxxx>`

Apply (or dry-run) the language service's code fixes for given diagnostics, batch-wide. `--fix-all` applies a fix-all action per code.

#### `check watch`

Stream diagnostics deltas as ndjson while files change (CI tail mode / agent loop mode).

---

### 4.8 `tsagent context` — LLM context packing

#### `context pack <target…> --token-budget N`

Given symbols, emit the minimal set of source slices that lets an LLM work on them: the definition, the types it references (transitively, budget permitting), and key usage examples — trimmed to the budget with a manifest of what was elided.

- `--for edit|review|explain` — tunes the slice priorities (edit wants callers; explain wants types).
- `--format text` packs with file-path headers, ready to paste.

#### `context expand <file:line:col> --radius semantic`

Smart context window: instead of ±N lines, expand to semantic boundaries (enclosing function/class plus the types used in the selection).

#### `context delta --since <git-ref>`

Pack exactly what changed and its blast radius: changed symbols, their direct dependents, new/changed types. Purpose-built for "review this diff" prompts.

---

### 4.9 `tsagent api` — Public API surface tooling

#### `api surface <package|path>`

Extract the public API surface as a stable, sorted digest: every reachable export with its fully-resolved signature. Deterministic output — diffable across commits.

#### `api diff --base <git-ref> [--head <git-ref>]`

Breaking-change detection: diff two API surfaces and classify every change as `breaking | possibly-breaking | additive | internal`, with the rule that fired (removed export, narrowed param, widened return, …).

- `--fail-on breaking` exit-code contract for CI.

#### `api docs <target…>`

Structured doc extraction: JSDoc + resolved types merged into one JSON per symbol (what a docs generator or an agent answering "how do I call this" actually needs).

---

### 4.10 `tsagent serve` — Session daemon & admin

#### `serve [--socket <path>|--stdio]`

Start the session daemon: persistent program, checker caches, overlay store, symbol-ID registry, file watcher. All CLI commands auto-discover and reuse a running session for their project.

- Protocol: JSON-RPC 2.0; every CLI command is also an RPC method with identical params/results (the CLI *is* a thin RPC client when a session exists).

#### `serve status` / `serve stop` / `serve reload`

Daemon admin. `status` reports memory, program version, overlay count, cache hit rates.

#### `serve overlay set <file> [--from-stdin|--from <path>]` / `overlay drop <file…>` / `overlay list`

Manage speculative file contents directly (the primitive under `check --with-diff`).

#### `serve snapshot save <name>` / `snapshot restore <name>`

Named overlay snapshots — lets an agent explore two edit strategies and compare `check` results between them.

---

## 5. Command Index (quick reference)

| # | Command | One-liner |
|---|---|---|
| 1 | `map outline` | Symbol tree per file/folder, depth- and budget-controlled |
| 2 | `map files` | Program file inventory + `--why` inclusion chains |
| 3 | `map stats` | Quantitative repo overview |
| 4 | `map search` | Project-wide symbol search → symbol IDs |
| 5 | `nav def` | Batch go-to-definition/implementation |
| 6 | `nav refs` | Find references, classified by usage kind |
| 7 | `nav usages` | Grouped usages with context lines |
| 8 | `nav calls` | Incoming/outgoing call hierarchy |
| 9 | `nav types` | Type hierarchy incl. structural implementers |
| 10 | `nav graph` | Import graph + cycle detection |
| 11 | `nav path` | Reachability between two symbols |
| 12 | `nav shape` | Find functions by type signature |
| 13 | `nav export-graph` | Re-export/barrel flow of a symbol |
| 14 | `type at` | Expanded type at position(s) |
| 15 | `type assignable` | A-assignable-to-B check with elaboration |
| 16 | `type explain-error` | Structured decomposition of a diagnostic |
| 17 | `type complexity` | Per-type cost calc, ranking, and why |
| 18 | `type infer` | Suggest types from usage |
| 19 | `type coverage` | `any`/`unknown` density report |
| 20 | `type instantiations` | All instantiations of a generic |
| 21 | `type flow` | Narrowing trace for a variable |
| 22 | `refactor mv` | Move files/folders, imports updated |
| 23 | `refactor mv-symbol` | Move one symbol between files |
| 24 | `refactor rename` | Project-wide rename |
| 25 | `refactor extract` | Extract function/constant/type |
| 26 | `refactor inline` | Inline function/variable |
| 27 | `refactor signature` | Change signature + all call sites |
| 28 | `refactor exports` | Default ↔ named export conversion |
| 29 | `refactor modules` | CJS ↔ ESM conversion |
| 30 | `refactor organize-imports` | Normalize imports project-wide |
| 31 | `refactor safe-delete` | Delete only if unreferenced; cascade option |
| 32 | `refactor apply-edits` | Apply any edit set as one transaction |
| 33 | `analyze dead-code` | Dead code scan with confidence + fix plan |
| 34 | `analyze complexity` | Cyclomatic + cognitive + type hotspots |
| 35 | `analyze duplicates` | AST-based clone detection |
| 36 | `analyze side-effects` | Purity / side-effect analysis |
| 37 | `analyze exhaustiveness` | Missing union/enum cases |
| 38 | `analyze unused-deps` | package.json vs real imports |
| 39 | `analyze churn-risk` | High-refs × high-churn blast-radius list |
| 40 | `analyze barrel-cost` | Barrel file cost + direct-import rewrites |
| 41 | `analyze assertions` | Casts, `!`, ignores — the trust-boundary map |
| 42 | `diagram classes` | Class/interface diagrams |
| 43 | `diagram deps` | Dependency diagrams |
| 44 | `diagram calls` | Call-graph diagrams |
| 45 | `diagram flow` | Per-function control-flow graphs |
| 46 | `diagram state` | State machines from discriminated unions |
| 47 | `check` | Filtered batch diagnostics |
| 48 | `check --with-diff` | Speculative edit check (no disk writes) |
| 49 | `check fix` | Batch code-fix application |
| 50 | `check watch` | Streaming diagnostics deltas |
| 51 | `context pack` | Budgeted context slices per symbol |
| 52 | `context expand` | Semantic (not line-based) context windows |
| 53 | `context delta` | Changed symbols + blast radius since a ref |
| 54 | `api surface` | Deterministic public-API digest |
| 55 | `api diff` | Breaking-change classification between refs |
| 56 | `api docs` | JSDoc + resolved types as structured docs |
| 57 | `serve` | Session daemon (JSON-RPC, overlays, watcher) |
| 58 | `serve snapshot` | Compare alternative edit strategies |

---

## 6. Mapping to typescript-go internals

| tsagent family | Reuses | Net-new |
|---|---|---|
| `map` | `internal/ls` documentSymbol/navigation bar, workspace symbols | aggregation, depth control, text formatter, stats |
| `nav def/refs` | `internal/ls` definition/references/rename infrastructure | batching, usage-kind classification, symbol-ID registry |
| `nav calls/types` | call-hierarchy + implementations providers | depth-bounded tree assembly, structural-implementer search |
| `nav graph/path` | `internal/compiler` module resolution, program imports | graph store, SCC/cycle detection, path search |
| `type *` | `internal/checker` (type display, assignability elaboration, flow nodes) | structural JSON type encoding, complexity metrics, instantiation tracking hooks |
| `refactor *` | `internal/ls` rename/edits, `internal/format` | mv-symbol, signature change, transactions, diagnostics-delta gate |
| `analyze *` | checker symbols/refs, AST walkers | dead-code roots model, clone hashing, purity walker, churn join |
| `diagram *` | outputs of nav/type families | mermaid/dot emitters |
| `check` | program diagnostics, LSP overlay machinery | diff→overlay application, delta computation, watch ndjson |
| `context` | source files, refs, types | budgeted slicer, semantic expansion |
| `api` | declaration emit / checker exports | surface normalizer, diff classifier |
| `serve` | LSP server loop scaffolding (`internal/lsp`) | JSON-RPC method surface, session discovery, snapshots |

The deepest leverage is in `internal/checker` access — most of what makes these commands better than grep/jscodeshift (structural implementers, assignability elaboration, narrowing traces, instantiation lists) is checker state that the LSP protocol never exposes but a fork can.

---

## 7. Phasing (suggested)

1. **Phase 0 — skeleton**: `serve` + one-shot fallback, symbol-ID registry, `map outline`, `map search`, `nav def/refs`, `check`. *(The minimum loop an agent needs.)*
2. **Phase 1 — mutations**: transaction engine (`apply-edits`), `refactor rename/mv/organize-imports/safe-delete`, diagnostics-delta gate.
3. **Phase 2 — speculation**: overlays, `check --with-diff`, snapshots. *(Unlocks agent self-verification.)*
4. **Phase 3 — type intelligence**: `type at/assignable/explain-error/complexity`.
5. **Phase 4 — analysis & context**: `analyze dead-code/…`, `context pack/delta`, `nav graph/calls`.
6. **Phase 5 — surface & diagrams**: `api *`, `diagram *`, remaining refactors (`signature`, `modules`, `mv-symbol`).
