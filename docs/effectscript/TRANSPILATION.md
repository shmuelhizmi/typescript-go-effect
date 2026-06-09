# EffectScript Transpilation Rules

Normative desugaring from EffectScript constructs into TypeScript + the `effect`
library. Notation: `⟦x⟧` is the desugaring of `x`; `body'` is a body with all
EffectScript statements recursively desugared. Identifiers introduced by the
desugarer (`Effect`, `Layer`, `Context`, `Fiber`, `pipe`) refer to the auto-imported
bindings (SPEC §1.2).

These rules are **the** semantics of EffectScript (SPEC §13).

---

## 1. Declarations

### 1.1 `effect` declaration

```
⟦ effect f(params): A raises E requires R { body } ⟧ =
  const f = Effect.fn("f")(function* (params) { body' });
```

* The string literal is the declared name (used as the tracing span name).
* With a plain type annotation `: T`, the generated generator return type is checked
  against `T` via a synthesized satisfies-check (see §10).
* `export effect f ...` → `export const f = ...`;
  `export default effect f ...` → `const f = ...; export default f;`.

### 1.2 Decorated `effect` declaration

Decorators compose inside `Effect.fn`'s variadic pipe arguments, top decorator
**last** (so it is outermost, matching decorator intuition):

```
⟦ @d1 @d2 effect f(p): T { body } ⟧ =
  const f = Effect.fn("f", ⟦d2⟧, ⟦d1⟧)(function* (p) { body' });
```

Well-known bare names resolve to `Effect.*`: `@retry(s)` → `Effect.retry(s)`,
`@timeout(t)` → `Effect.timeout(t)`, `@withSpan(n)` → `Effect.withSpan(n)`,
`@uninterruptible` → `Effect.uninterruptible`, etc., unless the name is in scope.

### 1.3 `effect` expressions

```
⟦ effect (params) { body } ⟧ = Effect.fn(function* (params) { body' })
⟦ effect { body } ⟧          = Effect.gen(function* () { body' })
```

### 1.4 `effect` class methods

```
class C {
  effect m(p): T { body }
}
⟦…⟧ =
class C {
  m = Effect.fn("C.m")(function* (this: C, p) { body' }.bind(this));
}
```

Normative simplification: the method becomes an instance field holding the
`Effect.fn` value; `this` inside `body` refers to the instance (arrow-like capture
via the field initializer scope). `static effect m` becomes a static field with
`"C.m"` as span name.

## 2. Binds

Inside the enclosing generator:

```
⟦ const x <- e; ⟧        = const x = yield* ⟦e⟧;
⟦ let x <- e; ⟧          = let x = yield* ⟦e⟧;
⟦ const {p} <- e; ⟧      = const {p} = yield* ⟦e⟧;
⟦ const x: T <- e; ⟧     = const x: T = yield* ⟦e⟧;
⟦ <- e; ⟧                = yield* ⟦e⟧;
⟦ (<- e) ⟧               = (yield* ⟦e⟧)
```

Mixed declarator lists keep order: `const a <- ea, b = 1;` →
`const a = yield* ea, b = 1;`.

## 3. `raise`

```
⟦ raise e; ⟧       = return yield* Effect.fail(⟦e⟧);     // statement
⟦ raise e ⟧        = (yield* Effect.fail(⟦e⟧))           // expression, type never
⟦ raise.die e; ⟧   = return yield* Effect.die(⟦e⟧);
⟦ raise.die e ⟧    = (yield* Effect.die(⟦e⟧))
```

(The `return` in statement position is unreachable — `Effect.fail` never resumes —
but keeps TS control-flow analysis exact, e.g. for definite assignment.)

## 4. Typed `try`

Let `T = try { b } catch (e1: C1) { h1 } … catch (en: Cn ∪ …) { hn } [catch (e) { hAll }] [finally { f }]`.

```
⟦T⟧(statement) = yield* P;
⟦T⟧(expression) = (yield* P)
const r = ⟦T⟧ …  = const r = yield* P;
```

where `P` is built by piping:

```
P = Effect.gen(function* () { b' }).pipe(
      Effect.catchTag("Tag(C1)", (e1) => Effect.gen(function* () { h1' })),
      ...,
      Effect.catchTags({ "Tag(Cn_a)": handler_n, "Tag(Cn_b)": handler_n }), // unions share one handler fn
      Effect.catchAll((e) => Effect.gen(function* () { hAll' })),           // if present
      Effect.ensuring(Effect.gen(function* () { f' })),                     // if present
    )
```

* `Tag(C)` is the string-literal type of `C.prototype._tag` resolved by the checker
  (for `Data.TaggedError("X")` classes this is `"X"`). If it cannot be resolved to a
  string literal → error 18110.
* For a union annotation `catch (e: A | B)`, one handler function is generated and
  referenced for each tag in a single `Effect.catchTags` call.
* `return` inside `b`/handlers returns from `P` (the inner gen), making `T`'s value
  that return — *not* from the enclosing effect. To exit the enclosing effect from
  inside a `try`, bind the result and return it.
* Control-flow statements that cross the `try` boundary (`break`/`continue` to an
  outer loop) are **errors** (18112) in v1, since the body moves into a nested
  generator. (`yield*` of binds still works — nested gens compose.)

## 5. Services

```
⟦ service S { members } ⟧ =
  class S extends Context.Tag("«prefix»S")<S, { members }>() {}
```

`«prefix»` comes from the `@effectServicePrefix` pragma comment, default `""`.
`export` is carried over.

## 6. Layers

```
⟦ layer L: S { body } ⟧          = const L = Layer.effect(S, Effect.gen(function* () { body' }));
⟦ scoped layer L: S { body } ⟧   = const L = Layer.scoped(S, Effect.gen(function* () { body' }));
⟦ layer L: S = e ⟧               = const L = Layer.succeed(S, ⟦e⟧);
⟦ layer L: S provide [A, B] { body } ⟧ =
  const L = Layer.effect(S, Effect.gen(function* () { body' })).pipe(Layer.provide([⟦A⟧, ⟦B⟧]));
```

## 7. Resources

```
⟦ using c <- acq release (p, exit?) { r } ⟧ =
  const c = yield* Effect.acquireRelease(⟦acq⟧, (p, exit?) => Effect.gen(function* () { r' }));

⟦ using c <- e; ⟧                 = const c = yield* ⟦e⟧;   // e must need Scope; checked, error 18130
⟦ defer { body } ⟧                = yield* Effect.addFinalizer(() => Effect.gen(function* () { body' }));
⟦ defer (exit) { body } ⟧         = yield* Effect.addFinalizer((exit) => Effect.gen(function* () { body' }));
```

## 8. Concurrency

```
⟦ fork e ⟧                 = Effect.fork(⟦e⟧)
⟦ join f ⟧                 = Fiber.join(⟦f⟧)
⟦ par [e1, …, en] ⟧        = Effect.all([⟦e1⟧, …, ⟦en⟧], { concurrency: "unbounded" })
⟦ par { k1: e1, … } ⟧      = Effect.all({ k1: ⟦e1⟧, … }, { concurrency: "unbounded" })
⟦ par(n) [ … ] ⟧           = Effect.all([…], { concurrency: ⟦n⟧ })
⟦ race [e1, e2] ⟧          = Effect.race(⟦e1⟧, ⟦e2⟧)
⟦ race [e1, …, en] ⟧ (n>2) = Effect.raceAll([⟦e1⟧, …, ⟦en⟧])
```

These produce Effects; combine with bind: `const [a,b] <- par [ea, eb]` →
`const [a, b] = yield* Effect.all([ea, eb], { concurrency: "unbounded" });`.

## 9. Pipeline `|>`

```
⟦ a |> f ⟧    = ⟦f⟧(⟦a⟧)
⟦ a |> f |> g ⟧ = ⟦g⟧(⟦f⟧(⟦a⟧))
```

Note `a |> f(b)` is `f(b)(a)` by the first rule (the RHS is the call expression
`f(b)`). Left-assoc, no special-casing: the RHS desugars first, then is applied.

When a chain has ≥ 3 stages the emitter MAY emit `pipe(a, f, g, h)` instead of
nested calls — both are normative-equivalent; `pipe` is preferred for readability of
output and matches hand-written Effect style.

## 10. Type-annotation checking

For `effect f(): A raises E requires R { body }` the emitted code is augmented (in
checking only, not in JS output) with:

```ts
const f = Effect.fn("f")(function* (…) { … }) satisfies (…args: any) => Effect.Effect<A, E, R>;
```

so channel mismatches surface as ordinary assignability errors pointing at the
annotation. For plain `: T` annotations, `satisfies (…) => T`.

## 11. Auto-import synthesis

After desugaring a file, for each referenced helper namespace not already imported:

```ts
import { Effect } from "effect";          // and/or Layer, Context, Fiber, pipe
```

with `"effect"` replaced by `effectImportSource`. Synthesized specifiers merge into
one import declaration. A user import of e.g. `Effect` from anywhere suppresses
synthesis for that name (their binding wins — same as classic JSX factory lookup).

## 12. Source maps & original positions

Every synthesized node maps back to the originating EffectScript token range:
`yield*` to the `<-` token, `Effect.fail` to the `raise` keyword, `Effect.fn` call
to the `effect` keyword, handler closures to their `catch` clauses. Checker
diagnostics on synthesized nodes are re-homed to those original ranges (mechanism:
`ast.Node.Original` links, as the JSX and async transforms already do).
