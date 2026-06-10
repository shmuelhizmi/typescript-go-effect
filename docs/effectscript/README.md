# EffectScript

**EffectScript** is a superset of TypeScript that adds first-class syntax for the
[Effect](https://effect.website) library — the same way JSX adds first-class syntax for
React. JSX elements are pure sugar that transpile into `React.createElement` /
`jsx(...)` calls; EffectScript constructs are pure sugar that transpile into
`Effect.gen`, `Effect.fn`, `Layer.effect`, `Effect.catchTag`, and friends.

It is implemented as an extension of the TypeScript compiler in this repository
(`typescript-go`), exactly as JSX is: new file extensions, a handful of new tokens and
AST nodes, a desugaring transform, and type checking of the result with the ordinary
TypeScript checker.

```
┌──────────────┐   parse    ┌─────────────────┐   desugar    ┌──────────────────┐
│  hello.ets   │ ─────────► │ EffectScript AST │ ───────────► │ plain TS calling │ ──► js + d.ts
│  hello.etsx  │            │ (new node kinds) │              │ the effect lib   │
└──────────────┘            └─────────────────┘              └──────────────────┘
```

## Why

Effect is "the missing standard library for TypeScript", but its generator-based
do-notation is noisy and its power features (errors, services, layers, concurrency,
resources) are all expressed through combinators. EffectScript gives every major
Effect feature a dedicated, readable syntax while emitting exactly the idiomatic
Effect code you would have written by hand.

## Ten-second tour

```ts
// greet.ets
service Greeter {
  greeting: Effect.Effect<string>
}

effect hello(name: string): string raises HttpError requires Greeter {
  greeter <- Greeter                 // bind a service (immutable declaration)
  prefix  <- greeter.greeting        // bind an effect
  if (name === "") raise new HttpError({ status: 400 })
  return `${prefix}, ${name}!`
}
```

transpiles to

```ts
import { Effect, Context } from "effect";

class Greeter extends Context.Tag("Greeter")<Greeter, {
  greeting: Effect.Effect<string>;
}>() {}

const hello = Effect.fn("hello")(function* (name: string) {
  const greeter = yield* Greeter;
  const prefix = yield* greeter.greeting;
  if (name === "") return yield* Effect.fail(new HttpError({ status: 400 }));
  return `${prefix}, ${name}!`;
});
```

## Status: implemented and running

The language is implemented in this compiler (parse-time lowering in
`internal/parser/effectscript.go`) and validated end-to-end: the
[integration program](../../testdata/tests/cases/effectscript/integration/quickstart.ets)
compiles under `strict` against the real `effect` npm package and runs on Node
with the expected output. Working today, in `.ets`/`.etsx` files:

`effect` declarations/blocks/anonymous fns/class methods · bare immutable binds
(`x <- e`, destructuring, typed, `(<- e)`) · `raise`/`raise.die` ·
postfix `catch { Tag as e >> … }` arms · `match (x)` / `match tag (x)` with
guards and or-patterns · `service`/`layer`/`scoped layer`/`provide` ·
`tagged error`/`schema` declarations · `par`/
`race`/`fork`/`join` · `using … release`/`defer` · the `|>` pipeline ·
`A raises E requires R` type sugar · auto-imports · dedicated
diagnostics (18100/18101/18150/18151) · golden-corpus acceptance tests
(`testdata/tests/cases/effectscript/`).

See [IMPLEMENTATION-PLAN.md](./IMPLEMENTATION-PLAN.md) for the status table and
known v0 deviations.

## The documents

| Document | Contents |
| --- | --- |
| [SPEC.md](./SPEC.md) | The language specification: file types, lexical additions, every construct and its semantics |
| [GRAMMAR.md](./GRAMMAR.md) | EBNF grammar deltas over the TypeScript grammar |
| [TRANSPILATION.md](./TRANSPILATION.md) | Normative desugaring rules — exact before/after for every construct |
| [FEATURE-MATRIX.md](./FEATURE-MATRIX.md) | Coverage map of Effect library features → EffectScript syntax |
| [EXAMPLES.md](./EXAMPLES.md) | Worked end-to-end programs (services, layers, concurrency, React/JSX interop) |
| [IMPLEMENTATION-PLAN.md](./IMPLEMENTATION-PLAN.md) | Phased plan to implement EffectScript inside this compiler |

## Design principles

1. **Sugar, not magic.** Every construct has a single, deterministic, local desugaring
   into public Effect APIs. No new runtime, no semantic deviation from the library.
2. **JSX-grade integration.** New file extensions (`.ets`, `.etsx`), compiler options
   (`effect`, `effectImportSource`) mirroring `jsx` / `jsxImportSource`, and full
   editor support through the existing language server.
3. **TypeScript stays TypeScript.** Every valid `.ts`/`.tsx` file is a valid
   `.ets`/`.etsx` file with identical meaning. EffectScript syntax is only ever
   enabled by the new extensions; existing code is untouched.
4. **Checked after desugaring.** The standard checker type-checks the desugared
   program, so inference of the `A`, `E`, `R` channels is exactly Effect's own —
   plus dedicated diagnostics for misused EffectScript constructs.

## Migrating existing code: `tsgo --effectify`

The compiler ships a reverse-direction codemod that rewrites classic
effect-library TypeScript into EffectScript syntax:

```sh
tsgo --effectify "src/**/*.ts"            # dry run: prints a diff per file
tsgo --effectify --write "src/**/*.ts"    # writes foo.ets and removes foo.ts
tsgo --effectify --check "src/**/*.ts"    # CI mode: exit 1 if anything would convert
```

It inverts the rules in [TRANSPILATION.md](./TRANSPILATION.md):
`Effect.gen`/`Effect.fn` become `effect` blocks and declarations, `yield*`
becomes `<-` binds, `Effect.fail` becomes `raise`, `Context.Tag` classes become
`service`, `Layer.effect/scoped/succeed` become `layer` declarations,
catch/`Match` pipe chains become postfix `catch` arms and `match` expressions,
and `Effect.Effect<A, E, R>` annotations become `A raises E requires R` where
they read well.

Rewriting `pipe(a, f)` / `a.pipe(f)` chains into `a |> f` is opt-in via
`--pipes`: the `|>` operator desugars to nested calls (`f(a)`), which loses
the left-to-right type inference that effect's `pipe()` overloads provide —
lambda stages such as `Arr.findFirst((x) => …)` only infer their parameter
type from the piped-in value in the `pipe()` form, so the rewrite can turn a
cleanly-checking file into one full of implicit-`unknown` errors.

`--write` renames files but does not touch references to them: import
specifiers written with an explicit `.ts` extension, `package.json`
`exports`/`main` entries and tsconfig `include` globs that name the migrated
files must be updated separately.

The migration is conservative by construction: EffectScript is a strict
superset of TypeScript, so any shape the tool does not confidently recognize is
left verbatim. Every converted file is verified before it is reported — the
output is re-parsed as EffectScript (which runs the forward desugarer) and the
result must be structurally equivalent to the original program; files that fail
verification are skipped wholesale and reported with a reason. Implementation
lives in `internal/effectify/`, with a golden corpus under
`testdata/tests/cases/effectscript/effectify/`.
