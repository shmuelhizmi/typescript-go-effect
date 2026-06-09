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
