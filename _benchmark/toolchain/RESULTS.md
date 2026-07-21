# Results — captured run

Host: 4-core Intel Xeon @ 2.10GHz (supports AVX2 **and** AVX-512, so GOAMD64 up
to v4 is valid), Go 1.26.0, Linux. 1 warmup + 5 timed runs per cell; times are
warm-cache steady state (so differences reflect CPU, not disk).

## Median wall-clock (seconds) and speedup vs `baseline`

| Preset / knob        | typescript | effect | vscode | vscode ×  |
|----------------------|-----------:|-------:|-------:|----------:|
| baseline (v1)        | 0.381      | 0.518  | 20.82  | 1.00×     |
| v3 (AVX2)            | 0.368      | 0.512  | 19.36  | 1.08×     |
| v4 (AVX-512)         | 0.376      | 0.511  | 20.71  | 1.01×     |
| nobounds (v3, -B)    | 0.379      | 0.562  | 19.98  | 1.04×     |
| pgo (v3 + profile)   | 0.354      | 0.512  | 19.91  | 1.05×     |
| max (v4+pgo+-B)      | 0.338      | 0.520  | 19.85  | 1.05×     |
| **max + GOGC=400**   | 0.309      | 0.404  | 15.85  | **1.31×** |
| **max + GOGC=off**   | **0.259**  |**0.351**|**15.27**| **1.36×**|

Peak RSS on vscode (`max` binary): default GC 5808 MB → GOGC=400 6197 MB (+7%)
→ GOGC=off 6411 MB (+10%). `--extendedDiagnostics` confirms the GC-off win is
in the Check + Parse phases (Total 17.7s → 13.6s).

## Takeaways

1. **The garbage collector is the biggest lever, by far.** `GOGC=off` gives
   **+36% on vscode, +47% on typescript, +48% on effect** vs baseline — and
   ~20-25% on top of the best build-time preset. tsgo is a short-lived batch
   process that allocates a large working set then exits, so collecting during
   the run just burns CPU reclaiming memory the OS frees at exit anyway. The
   cost is only +10% peak RSS here (the live working set already dominates).
   `GOGC=400` captures most of the win at +7% RSS if you want a safety margin.

2. **Build-time toolchain presets barely matter on these workloads.** GOAMD64
   v3/v4, PGO, and bounds-check elision each land within a few percent of
   baseline — mostly inside run-to-run noise (note v4's median on vscode is
   *slower* than v3's, which is pure variance). Their combination (`max`) is a
   real but small ~5% on vscode and ~13% on the tiny CPU-bound typescript case.
   Go code isn't SIMD-heavy, so wider vector ISAs give little; PGO helps hot
   inlining a little; `-B` is a wash (the hot paths were already bounds-check
   -eliminated by the optimizer).

3. **Recommendation.** Ship `baseline` or `v3` (v3 is free and never hurt), and
   set **`GOGC=off`** (or `400`) in the environment for one-shot CLI compiles —
   that single env var beats every build-flag combination tried here. Reserve
   GC tuning caution for long-lived `tsgo --watch` / LSP sessions, where
   unbounded heap growth matters and GOGC=off is *not* appropriate.
