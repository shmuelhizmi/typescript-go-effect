# EffectScript Transpilation Rules

Normative desugaring from EffectScript constructs into TypeScript + the `effect`
library. Notation: `⟦x⟧` is the desugaring of `x`; `body'` is a body with all
EffectScript statements recursively desugared. Identifiers introduced by the
desugarer (`Effect`, `Layer`, `Context`, `Fiber`, `Match`, `pipe`) refer to the auto-imported
bindings (SPEC §1.2).

These rules are **the** semantics of EffectScript (SPEC §14).

---

## 1. Declarations

### 1.1 `effect` declaration

```
⟦ effect f(params): A raises E requires R { body } ⟧ =
  const f = Effect.fn("f")(function* (params) { body' });
```

* The string literal is the declared name (used as the tracing span name).
* With a plain type annotation `: T`, the generated generator return type is checked
  against `T` via a synthesized satisfies-check (see §11).
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

Inside the enclosing generator (binds are always immutable — they emit `const`):

```
⟦ x <- e; ⟧              = const x = yield* ⟦e⟧;
⟦ {p} <- e; ⟧            = const {p} = yield* ⟦e⟧;
⟦ [p] <- e; ⟧            = const [p] = yield* ⟦e⟧;
⟦ x: T <- e; ⟧           = const x: T = yield* ⟦e⟧;
⟦ <- e; ⟧                = yield* ⟦e⟧;
⟦ (<- e) ⟧               = (yield* ⟦e⟧)
```

## 3. `raise`

```
⟦ raise e; ⟧       = return yield* Effect.fail(⟦e⟧);     // statement
⟦ raise e ⟧        = (yield* Effect.fail(⟦e⟧))           // expression, type never
⟦ raise.die e; ⟧   = return yield* Effect.die(⟦e⟧);
⟦ raise.die e ⟧    = (yield* Effect.die(⟦e⟧))
```

(The `return` in statement position is unreachable — `Effect.fail` never resumes —
but keeps TS control-flow analysis exact, e.g. for definite assignment.)

## 4. Postfix `catch` arms

Let `K = expr catch { A1 … An }` where arm `Ai` is
`Ci_1 | … | Ci_k [as ei] >> bi` and the optional final arm is `_ [as e] >> bAll`.

```
⟦K⟧ = ⟦expr⟧.pipe(
        H(A1),
        ...,
        Effect.catchAll((e) => B(bAll)),          // if a '_' arm is present
      )
```

per-arm handler `H`:

```
H( C as e >> b )            = Effect.catchTag("Tag(C)", (e) => B(b))
H( C1 | C2 as e >> b )      = Effect.catchTags({ "Tag(C1)": h, "Tag(C2)": h })
                              where h = (e) => B(b)     // one shared function
```

arm-body lowering `B`:

```
B( expression b )  = Effect.gen(function* () { return ⟦b⟧'; })
B( { body } )      = Effect.gen(function* () { body' })
```

(The emitter MAY simplify `B(b)` to `Effect.succeed(b)` / `Effect.fail(x)` when the
arm body is statically effect-free — e.g. a pure expression or a lone `raise`.
Both outputs are normative-equivalent.)

* `Tag(C)` is the string-literal type of `C.prototype._tag` resolved by the checker
  (for `Data.TaggedError("X")` classes this is `"X"`). If it cannot be resolved to a
  string literal → error 18110.
* An omitted `as` binding desugars with a fresh unused parameter name.
* `K` is an expression of Effect type; it composes with binds:
  `x <- e catch { … }` → `const x = yield* ⟦K⟧;`.
* `return` inside a `{ body }` arm returns from that arm's `Effect.gen` (it is the
  recovery value), not from the enclosing effect.
* `break`/`continue` referencing loops outside the arm are errors (18112).

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

These produce Effects; combine with bind: `[a, b] <- par [ea, eb]` →
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

## 10. `match` expressions

Let `M = match [value|tag] (x) { A1 … An }` with arms `Ai = Pi [if gi] >> bi`.

Pure context (outside effect bodies):

```
⟦M⟧ = Match.value(⟦x⟧).pipe(
        W(A1), ..., W(An),
        T,
      )
```

where the terminator `T` is `Match.exhaustive` if no catch-all/binding arm exists,
otherwise the last arm lowers into `Match.orElse`:

```
W( _ as e >> b )                = Match.orElse((e) => ⟦b⟧)            // last arm only
W( ident >> b )                 = Match.orElse((ident) => ⟦b⟧)        // lowercase binding
W( Tag as e >> b )              = Match.tag("Tag(C)", (e) => ⟦b⟧)
W( Tag1 | Tag2 as e >> b )      = Match.tags({ "Tag(C1)": h, "Tag(C2)": h })
W( lit >> b )                   = Match.when(lit, () => ⟦b⟧)
W( lit1 | lit2 >> b )           = Match.whenOr(lit1, lit2, () => ⟦b⟧)
W( {f1: p1, g} >> b )           = Match.when(S({f1: p1, g}), (v) => { const {f1, g} = v; return ⟦b⟧ })
                                  // S(…) = structural pattern: literal fields stay,
                                  // binding fields are dropped from the test object
W( [p, ...rest] >> b )          = Match.when((v): v is τ => Array.isArray(v) && «structural tests»,
                                             (v) => { const [p, ...rest] = v; return ⟦b⟧ })
W( P if g >> b )                = Match.when((v) => «test of P»(v) && ((«bindings of P») => g)(v),
                                             (v) => { «destructure P»; return ⟦b⟧ })
```

`match tag (x)` requires every `Pi` to be a TagReference; lowering is identical
(all arms via `Match.tag`/`Match.tags`), and exhaustiveness is over the scrutinee
union's `_tag`s.

Effectful context (inside an effect body, when any arm uses `<-`/`raise` or a
block body): every arm body lowers through `B(...)` from §4 (arm bodies become
`Effect.gen`s; statically pure arms MAY simplify to `Effect.succeed`), making the
match select an *Effect*; the whole expression is then bound:
`⟦M⟧ₑ = (yield* ⟦M with B-lowered arms⟧)`. If no arm is effectful, the pure form is
used unchanged.

* Arm order is preserved; first match wins.
* An arm after a catch-all/binding arm → 18151.
* Pattern identifier case rule (lowercase binding / Capitalized tag) → 18150.

## 11. Type-annotation checking

For `effect f(): A raises E requires R { body }` the emitted code is augmented (in
checking only, not in JS output) with:

```ts
const f = Effect.fn("f")(function* (…) { … }) satisfies (…args: any) => Effect.Effect<A, E, R>;
```

so channel mismatches surface as ordinary assignability errors pointing at the
annotation. For plain `: T` annotations, `satisfies (…) => T`.

## 12. Auto-import synthesis

After desugaring a file, for each referenced helper namespace not already imported:

```ts
import { Effect } from "effect";          // and/or Layer, Context, Fiber, Match, pipe
```

with `"effect"` replaced by `effectImportSource`. Synthesized specifiers merge into
one import declaration. A user import of e.g. `Effect` from anywhere suppresses
synthesis for that name (their binding wins — same as classic JSX factory lookup).
A non-import user binding that shadows a needed helper name (e.g. a local
`const Fiber = …` in a file using `join`) collides with the synthesized import and
surfaces as a normal redeclaration error — rename the local or import the helper
yourself. (Aliasing the synthesized import is a possible future refinement.)

## 13. Source maps & original positions

Every synthesized node maps back to the originating EffectScript token range:
`yield*` to the `<-` token, `Effect.fail` to the `raise` keyword, `Effect.fn` call
to the `effect` keyword, handler closures to their `catch` clauses. Checker
diagnostics on synthesized nodes are re-homed to those original ranges (mechanism:
`ast.Node.Original` links, as the JSX and async transforms already do).
