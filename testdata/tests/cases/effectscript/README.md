# EffectScript golden corpus

Acceptance suite for the EffectScript implementation (see
`docs/effectscript/IMPLEMENTATION-PLAN.md`, Phase 0). The test runner does not pick
this directory up yet; Phase 3 wires it into `internal/testrunner` as a new suite.

## Layout

* `emit/<case>.ets[x]` — EffectScript input
* `emit/<case>.expected.ts[x]` — required transpilation (modulo whitespace and
  trivia; comparison is AST-structural once the suite is wired up)
* `diagnostics/<case>.ets` — inputs that must produce the diagnostics listed in the
  trailing `//// [errors]` block of the same file

Each emit case isolates one spec area; `kitchenSink` exercises interactions.
The expected outputs assume `effect: "transform"`, `effectImportSource: "effect"`.
