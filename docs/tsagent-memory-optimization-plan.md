# tsagent Memory Optimization Plan

## Scope

Investigate `tsagent` memory growth on large projects, starting with:

`/Users/shmuelhizmi/.superset/worktrees/spring/rpc-over-bin/tsconfig.json`

The target project has broad root `tsconfig` include globs, `allowJs: true`, and about:

- 9,619 TS/TSX files outside obvious generated folders
- 3,549 JS/JSX files outside obvious generated folders
- 15,043 program files reported by `tsagent map files`

Measurements below were taken on macOS with `/usr/bin/time -l`. `maximum resident set size`
is the RSS figure. `peak memory footprint` is also noted for perf because it tracks the
reported 50 GB class behavior more closely.

## Measurements

| Probe | Wall time | Max RSS | Peak footprint | Notes |
| --- | ---: | ---: | ---: | --- |
| `map files --limit 1` | 1.03s | 1.58 GB | 1.57 GB | Baseline workspace/program construction |
| `report --include file-size --top 1` | 0.79s | 1.76 GB | 1.75 GB | Lightweight structure provider |
| `report structure --top 1` | 1.22s | 1.92 GB | 1.91 GB | All structure providers |
| `report quality --top 1` | 68.73s | 11.47 GB | 11.46 GB | Default quality group |
| `report --include duplicates --top 1` | 1.04s | 1.66 GB | 1.65 GB | Not a culprit |
| `report --include complexity --top 1` | 35.66s | 8.35 GB | 8.35 GB | Main quality culprit |
| `report --include assertions --top 1` | 31.40s | 5.12 GB | 5.11 GB | Secondary quality culprit |
| `report --include exhaustiveness --top 1` | 1.49s | 2.14 GB | 2.12 GB | Modest |
| `report --include barrel-cost --top 1` | 3.42s | 1.94 GB | 1.93 GB | Modest |
| `report --include side-effects --top 1` | 1.00s | 1.91 GB | 1.90 GB | Modest |
| `report --include unused-deps --top 1` | 0.75s | 1.52 GB | 1.51 GB | Modest |
| `report perf --top 1` | 129.08s | 23.34 GB | 43.44 GB | High-risk path |
| `report perf --single-threaded --top 1` | 59.31s | 20.45 GB | 25.24 GB | Better than default on this project |

`report full` was intentionally not run yet. `report perf` alone is already near the
reported footprint range, and `report full` would add the quality hot spots afterward.

## Implemented Results

Changes implemented after the baseline:

- Added `_packages/tsagent/scripts/measure-memory.mjs`, a repeatable `/usr/bin/time -l`
  harness that builds or accepts a tsagent binary, runs named probes, and writes
  per-probe stdout, timing files, `summary.json`, and `summary.md`.
- The assertions report uses an AST-only count path instead of the detailed
  type-stringing analyzer.
- The complexity report uses bounded type scoring: all functions get syntactic
  branch metrics, but only the top syntactic candidates get checker-backed type
  complexity.
- Report tables honor `--top` more consistently for quality rows and directory
  rollups.
- Perf capture parsing is split so trace events are parsed first, then the fresh traced
  compiler program is released before decoding large `types_N.json` payloads.
- `report perf` defaults to single-threaded capture for lower peak memory; the old
  parallel checker capture is available via `--parallel-perf`.
- Perf-only reports release the initial workspace program before building the traced
  program, avoiding two retained compiler programs for that command.
- Perf trace/type artifacts are written to a temporary file-backed trace sink instead of
  retained as strings in an in-memory filesystem.
- `report perf` uses a config-only workspace, so it parses the project config without
  constructing the normal Program/LanguageService before the traced perf compile.
- Generic `report --include perf` also uses a config-only workspace after flag parsing,
  so the explicit include spelling no longer builds an unused warm program.
- Standalone `perf summary`, `perf hot-files`, `perf hot-types`, `perf hot-checks`, and
  `perf depth-limits` also use config-only workspaces before their traced compiles and
  default to the low-memory single-checker capture, with `--parallel-perf` as the
  explicit high-memory override.
- Combined reports build non-perf providers first, then release the one-shot workspace
  program before running perf, while preserving the original page order.
- Persistent daemon workspaces are marked non-releasable so `--connect` report commands
  cannot accidentally drop the session program.
- Perf trace/type JSON arrays are streamed from the temporary files, and type descriptors
  are aggregated as they are decoded instead of retained as a full descriptor slice.
- Perf type-origin maps are narrowed to only the type IDs referenced by sampled checker
  spans or depth-limit instants; hot-type and per-file counts still see every descriptor.
- Normalized trace spans retain decoded `args` only for sampled checker operations without
  a direct file path; hot-file spans no longer keep their JSON argument maps.
- Perf trace event `args` now decode into a typed compact struct instead of a generic
  `map[string]any`, avoiding per-event map allocation while retaining the keys needed for
  file attribution, type-origin attribution, and depth-limit details.
- Added `testdata/tsagent/benchmark-example-typesystem`, a small dependency-free fixture
  for fast iteration on type-system-heavy perf reports before re-running the large
  `rpc-over-bin` project.
- Tracing now writes `types_N.json` descriptor files in bounded chunks instead of building
  the full JSON file in a `strings.Builder` while the traced Program is still live.
- `tsagent` perf capture disables unused type `display` text in `types_N.json`; normal
  `--generateTrace` output keeps display text by default.
- `tsagent` perf capture streams type descriptors in chunks after checked files complete,
  releasing the tracer's `TracedType` references instead of holding every recorded type
  until `StopTracing`.
- Perf capture runs a bounded GC cadence during large type-descriptor flush loops. The
  measured default uses one forced GC per 1024 checked files, which keeps the large full
  report below 7 GB with moderate wall-time cost.
- Default project-scoped perf reports no longer build the all-libraries hot-type map
  unless `--include-libs` is requested.
- Perf releases the traced Program before parsing retained trace events; hot-check line
  numbers are resolved lazily from workspace file text.

Measured with the optimized binary (`/tmp/tsagent-opt`) on the same project:

| Probe | Before | After | Change |
| --- | ---: | ---: | --- |
| `report --include assertions --top 1` wall | 31.40s | 1.03s | 30.5x faster |
| `report --include assertions --top 1` RSS | 5.12 GB | 1.54 GB | -70% |
| `report --include complexity --top 1` wall | 35.66s | 3.70s | 9.6x faster |
| `report --include complexity --top 1` RSS | 8.35 GB | 2.60 GB | -69% |
| `report quality --top 1` wall | 68.73s | 14.39s | 4.8x faster |
| `report quality --top 1` RSS | 11.47 GB | 3.91 GB | -66% |
| `report structure --top 1` wall | 1.22s | 1.41s | +16% |
| `report structure --top 1` RSS | 1.92 GB | 1.80 GB | -6% |
| `report perf --top 1` wall | 129.08s | 82.22s | 1.6x faster |
| `report perf --top 1` RSS | 23.34 GB | 16.00 GB | -31% |
| `report perf --top 1` footprint | 43.44 GB | 16.41 GB | -62% |
| `report --include perf --top 1` wall | not measured | 70.63s | now measured |
| `report --include perf --top 1` RSS | not measured | 12.68 GB | now measured |
| `report --include perf --top 1` footprint | not measured | 16.33 GB | now measured |
| `perf summary` wall | not measured | 61.97s | now measured |
| `perf summary` RSS | not measured | 14.94 GB | now measured |
| `perf summary` footprint | not measured | 16.83 GB | now measured |
| `report full --top 1` wall | not run safely | 82.50s | now verified |
| `report full --top 1` RSS | not run safely | 15.65 GB | now verified |
| `report full --top 1` footprint | not run safely | 16.28 GB | now verified |

The final `report perf` sample favors RSS reduction over the fastest intermediate sample.
RSS on these large perf runs is noisy across passes; footprint has been the more stable
signal for the original 50 GB problem.

One attempted optimized parallel perf run (`--parallel-perf` equivalent) was stopped
after 343.99s because it was still consuming CPU and had already regressed runtime.
That reinforced making the lower-memory capture the default report mode.

Additional current-source samples after typed trace-args decoding:

| Probe | Previous optimized | Typed trace args | Change |
| --- | ---: | ---: | --- |
| `report perf --top 1` wall | 82.22s | 76.95s | -6% |
| `report perf --top 1` RSS | 16.00 GB | 9.74 GB | -39% |
| `report perf --top 1` footprint | 16.41 GB | 17.10 GB | +4% |
| `perf summary` wall | 61.97s | 86.94s | +40% |
| `perf summary` RSS | 14.94 GB | 9.95 GB | -33% |
| `perf summary` footprint | 16.83 GB | 16.36 GB | -3% |

The typed trace-args change is a real RSS win, but it is not yet the required 20-30%
footprint reduction. Keep treating peak footprint as the acceptance signal for the
original 50 GB-class problem.

Additional samples after chunked type-file writes and disabling unused type display text
for `tsagent` perf capture:

| Probe | Prior optimized | Current | Change |
| --- | ---: | ---: | --- |
| `report perf --top 1` wall | 82.22s | 29.98s | 2.7x faster |
| `report perf --top 1` RSS | 16.00 GB | 8.99 GB | -44% |
| `report perf --top 1` footprint | 16.41 GB | 8.99 GB | -45% |
| `perf summary` wall | 61.97s | 31.74s | 2.0x faster |
| `perf summary` RSS | 14.94 GB | 9.89 GB | -34% |
| `perf summary` footprint | 16.83 GB | 9.89 GB | -41% |
| `report full --top 1` wall | 82.50s | 42.96s | 1.9x faster |
| `report full --top 1` RSS | 15.65 GB | 9.86 GB | -37% |
| `report full --top 1` footprint | 16.28 GB | 9.87 GB | -39% |

The `display` opt-out also removes TypeToString side effects from perf capture. On the
sampled target project, depth-limit markers changed from 677 to 676 and recorded type
counters dropped because the report no longer traces extra types created only while
formatting descriptor display strings. The perf report does not render `display`, so the
remaining fields still cover summary, hot files, hot types, hot checks, and depth limits.

Additional samples after chunked type-descriptor streaming and periodic GC in perf
capture:

| Probe | Prior optimized | Current | Change |
| --- | ---: | ---: | --- |
| `report full --top 1` wall | 42.96s | 52.99s | +23% |
| `report full --top 1` RSS | 9.86 GB | 5.69 GB | -42% |
| `report full --top 1` footprint | 9.87 GB | 5.71 GB | -42% |

The 2048-file GC interval was also measured at `47.34s`, `6.73 GB` RSS, and `6.84 GB`
footprint, but the committed 1024-file interval leaves more headroom under the 7 GB
target.

## Small Iteration Fixture

`testdata/tsagent/benchmark-example-typesystem` is a small TypeScript project intended
for quick optimization loops. It has no external dependencies and exercises generic
instantiation, conditional types, mapped types, template-literal unions, and a clear
hotspot in `src/hotspot.ts`.

Validated quick probes:

- `map files` lists the four source files.
- `check --severity error` returns `0 diagnostics`.
- `perf summary --single-threaded` completes in about `0.03s`, with about `31.8 MB`
  RSS and `22.3 MB` peak footprint in the sampled run.
- `perf hot-files --top 5 --single-threaded` ranks `src/hotspot.ts` first.
- `perf hot-types --top 10 --single-threaded` surfaces type-heavy symbols including
  `TypedSlot`, `ApiResult`, `DeepNormalize`, and `ExtractVerb`.
- `report perf --top 10 --single-threaded` writes a one-page HTML report.

## Remaining Hot Spots / Follow-up

### Perf report

`internal/tsagent/perf.Gather` remains the largest individual report provider, but is now
below the 7 GB full-report target on the measured project. The current implementation
releases compiler programs before type aggregation and no longer retains raw trace
strings, full type descriptor slices, generic event argument maps, full type JSON
builders, type display strings, all recorded `TracedType` references until `StopTracing`,
the all-libraries hot-type map by default, or a declaration-origin entry for every
recorded type. It still retains:

- all normalized trace spans
- all instant events
- typed args for sampled checker spans without direct file paths
- type-id origin maps and hot-type aggregate maps

I attempted direct trace-span aggregation into hot-file and hot-check structures, but it
regressed the measured perf path on this project (`60.76s`, `16.49 GB` RSS,
`16.60 GB` footprint in the final captured sample before reverting). The current code
keeps normalized spans because that path measured better after the type-origin narrowing.

I also attempted decoding a narrower custom type-descriptor shape. It improved wall time
but repeatedly worsened RSS (`55.61s`, `15.81 GB` RSS, `16.69 GB` footprint), so the code
keeps full descriptor decoding while still aggregating and discarding descriptors as they
stream.

I later retried a manual streaming summary parser for type descriptors. It again improved
wall time but regressed the large `perf summary` memory sample (`60.99s`, `15.53 GB` RSS,
`17.27 GB` footprint), so it was reverted.

I also tried detaching the type-tracer snapshot slice while dumping descriptors. It
slightly reduced RSS but worsened the report footprint sample (`14.18 GB` versus
`13.97 GB` for chunked type writes alone), so that tweak was reverted.

The old parallel perf capture remains available through `--parallel-perf`, but it was
not a good default for this project. A post-change parallel probe was stopped after
343.99s because runtime had already regressed badly.

### Quality complexity

The report path is now bounded and much cheaper. The detailed `analyze complexity` CLI
path still performs exhaustive type scoring, which is useful for explicit analysis but
can remain expensive on Effect-heavy codebases. A future public flag could expose the
bounded mode outside reports.

### Quality assertions

The report path is now count-only and AST-backed. The detailed `analyze assertions` CLI
path still acquires checkers and prints source/target types by design.

## Plan

### Phase 1: Measurement Harness

Implemented as:

`node _packages/tsagent/scripts/measure-memory.mjs --project <tsconfig> [--heavy]`

or, for direct before/after comparisons:

`node _packages/tsagent/scripts/measure-memory.mjs --project <tsconfig> --baseline-bin <old> --candidate-bin <new> [--heavy]`

The harness runs named probes, extracts wall time, RSS, and footprint, and keeps stdout,
timing logs, HTML reports, `summary.json`, and `summary.md` in `/tmp` by default.

Acceptance target:

- One command can compare a baseline binary and an optimized binary.
- Results are stable enough to detect a 20 percent RSS regression.
- The harness includes at least `map files`, individual Quality providers, `report perf`,
  and `report perf --single-threaded`.

### Phase 2: Low-risk Report Fixes

1. Make the report assertions provider use a count-only AST walk.
   - Do not acquire a checker.
   - Do not call `TypeToStringEx`.
   - Keep detailed type strings in `analyze assertions` CLI output.
   - Expected result: assertions report should drop from about 5.12 GB toward the
     1.7-2.0 GB baseline.

2. Respect `--top` in report-only table construction where possible.
   - Avoid retaining detailed rows that are not rendered.
   - This is not enough for perf, but it removes easy report overhead.

3. Consider making `report perf` default to single-threaded for large projects or when
   program file count crosses a threshold.
   - Based on the current probe, this can cut footprint by about 18 GB on this project.
   - Keep an override for users who prefer default parallel behavior.

### Phase 3: Bounded Complexity Analysis

Replace all-functions type scoring in report complexity with a bounded strategy:

1. First pass: compute branch metrics only for all functions.
2. Candidate selection: keep top `max(top * K, floor)` functions by syntactic score.
3. Second pass: compute type complexity only for candidates.
4. Add a separate flag or mode for exhaustive type-complexity scoring.

Suggested defaults:

- `K = 10`
- `floor = 200`
- report mode uses bounded scoring
- `analyze complexity` can keep exhaustive behavior behind an explicit flag if needed

Acceptance target:

- `report --include complexity --top 1` drops from 8.35 GB to under 3 GB on the target
  project.
- Top syntactic hotspots remain stable.
- Tests cover the bounded candidate behavior and the explicit exhaustive mode.

### Phase 4: Streaming Perf Capture

Reduce perf report transient memory:

1. Replace string-backed `memFS` for perf trace artifacts with a temp-dir or streaming
   sink.
2. Stream `trace.json` parsing instead of decoding the whole event array.
3. Aggregate hot files/checks/depth-limit counts while parsing instead of retaining all
   raw spans and instants when building the HTML report. An initial attempt regressed on
   this project, so this needs a more allocation-aware design before retrying.
4. Stream `types_N.json` descriptors and aggregate hot types plus type-id declaration
   lookup without retaining the full descriptor slice when possible.
5. Drop the traced `Program` reference before heavy report rendering once path and line
   lookup data needed by the report has been materialized.
6. Write `types_N.json` files in bounded chunks instead of one full-file builder.
7. Skip type descriptor display strings for `tsagent` perf capture because the report
   does not render them.
8. Stream and release perf type descriptors throughout checking, then force bounded GC
   during large capture loops to keep full-report footprint under the 7 GB target.

Acceptance target:

- `report perf --top 1` peak footprint under 12 GB on the target project.
- `report perf --single-threaded --top 1` peak footprint under 8 GB.
- Output pages remain equivalent for summary, hot files, hot types, hot checks, and depth
  limits.

Current result: `report perf --top 1` footprint is down from 43.44 GB to 8.99 GB on the
target project after the earlier display optimization, and `report full --top 1` is now
verified at 5.71 GB after chunked type-descriptor streaming plus periodic GC. This clears
the 12 GB target, the requested additional 20-30% reduction, and the follow-up 7 GB full
report target.

### Phase 5: Full Report Guardrails

Add guardrails before enabling expensive combined reports on very large projects:

- Print planned providers before running when `report full` is invoked on large projects.
- Consider automatic provider isolation for `report full`, where each provider can run in
  a separate subprocess and merge page data afterward. This prevents memory from one
  provider carrying into the next.
- Keep `dead-code` and `churn-risk` opt-in as they are today.
- Add clear output that suggests narrower reports when estimated memory is high.

Acceptance target:

- `report full --top 1` does not exceed the larger of 1.25x the biggest individual
  provider peak or a documented cap.
- A failed provider still writes successful pages without retaining its workspace state.

## Verification

Current validation:

`go test ./internal/tsagent/... ./cmd/tsagent`

`git diff --check`

`node _packages/tsagent/scripts/measure-memory.mjs --list`

Result: all passed after the optimization changes.

Rejected experiments were also measured and reverted:

- Direct trace-span aggregation reduced retained span data but worsened the perf report
  sample.
- Narrow custom type-descriptor decoding improved wall time but worsened RSS/footprint.
- Release-before-render for non-perf reports added GC cost and worsened the quality
  report sample.
- Bounded structure function ranking worsened the structure report wall-time sample.
