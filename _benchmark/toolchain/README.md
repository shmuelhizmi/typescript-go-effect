# tsgo toolchain-preset benchmarks

Measures how much different **Go-toolchain build presets** and **Go runtime
knobs** change tsgo's end-to-end typecheck time on three real TypeScript
projects. tsgo can't be built with gollvm/gccgo (their gofrontend has no
generics and is stuck ~Go 1.18; tsgo needs Go 1.26), so "toolchain presets"
here means variations of the standard `gc` toolchain, not an alternate compiler.

## Workloads

| Workload     | Command                                              | Character                       |
|--------------|------------------------------------------------------|---------------------------------|
| `typescript` | `tsgo -p _submodules/TypeScript/src/compiler --noEmit`| small, checker-heavy single proj |
| `effect`     | `tsgo -p packages/effect/tsconfig.json --noEmit`     | type-inference-heavy            |
| `vscode`     | `tsgo -p src/tsconfig.json --noEmit`                 | large (~1.5M LOC), 0 errors     |

`vscode` and `effect` need their `node_modules` installed (`npm install
--ignore-scripts` / `pnpm install --ignore-scripts`). Effect also needs
`@types/node` linked into `packages/effect/node_modules/@types/node`.
`typescript` only needs `@types/node` in the submodule (`npm install
--ignore-scripts` there). The effect/typescript workloads report a few
unresolved-module / cross-project errors; these are stable across runs and do
not stop the checker from doing full work.

## Presets

Build-time (recompile tsgo):

| Preset     | Build config                                     |
|------------|--------------------------------------------------|
| `baseline` | `go build` (GOAMD64=v1)                          |
| `v3`       | `GOAMD64=v3` (AVX2)                              |
| `v4`       | `GOAMD64=v4` (AVX-512)                           |
| `nobounds` | `GOAMD64=v3 -gcflags=all=-B` (elide bounds checks)|
| `pgo`      | `GOAMD64=v3 -pgo=<vscode cpu profile>`           |
| `max`      | `GOAMD64=v4 -pgo=<profile> -gcflags=all=-B`      |

Runtime (same binary, env var): `GOGC=off`, `GOGC=400` vs default `GOGC=100`.

## Run it

```bash
# 1. collect a PGO profile from a representative run
(cd <vscode> && tsgo -p src/tsconfig.json --noEmit --pprofDir /tmp/pgo)
# 2. build all presets
PGO_PROFILE=/tmp/pgo/*-cpuprofile.pb.gz ./build_presets.sh
# 3. benchmark
VSCODE_DIR=<vscode> EFFECT_DIR=<effect> RUNS=5 ./run_bench.sh
```

Results land in `results/results.csv`. See `RESULTS.md` for a captured run.
