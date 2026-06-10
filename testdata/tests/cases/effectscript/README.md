# EffectScript golden corpus

Acceptance suite for the EffectScript implementation (see
`docs/effectscript/IMPLEMENTATION-PLAN.md`). The emit corpus runs in
`internal/parser/effectscript_corpus_test.go` (`go test ./internal/parser/ -run
TestEffectScriptCorpus`); set `UPDATE_EFFECT_BASELINES=1` to regenerate the
`.expected` files after an intentional lowering change.

## Layout

* `emit/<case>.ets[x]` — EffectScript input
* `emit/<case>.expected.ts[x]` — required transpilation (modulo whitespace and
  trivia; comparison is AST-structural once the suite is wired up)
* `diagnostics/<case>.ets` — inputs that must produce the diagnostics listed in the
  trailing `//// [errors]` block of the same file

Each emit case isolates one spec area; `kitchenSink` exercises interactions.
The expected outputs assume `effect: "transform"`, `effectImportSource: "effect"`.
