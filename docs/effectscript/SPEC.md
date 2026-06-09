# EffectScript Language Specification

Status: **Draft 1**
Target library: `effect` ≥ 3.x
Host language: TypeScript (as implemented by this repository)

EffectScript is a strict superset of TypeScript. This document specifies only the
delta. Anything not mentioned here behaves exactly as in TypeScript.

---

## 1. File types and modes

| Extension | Base behavior | EffectScript syntax | JSX syntax |
| --- | --- | --- | --- |
| `.ets` | `.ts` | ✅ | ❌ (`<T>e` assertions stay legal) |
| `.etsx` | `.tsx` | ✅ | ✅ |

* New `ScriptKind` values: `ETS`, `ETSX`.
* New `LanguageVariant` flag dimension: `EffectScript` (combines with `JSX` for
  `.etsx`).
* Declaration emit for `.ets`/`.etsx` produces ordinary `.d.ts`.
* Every syntactically valid `.ts` file is a valid `.ets` file with identical
  semantics, with one caveat: inside *effect bodies* (§3) the contextual keywords of
  §2.2 take precedence at statement head (see §2.2 for the exact carve-outs).

### 1.1 Compiler options

| Option | Values | Default | Mirrors |
| --- | --- | --- | --- |
| `effect` | `"transform"` \| `"preserve"` \| `"none"` | `"transform"` | `jsx` |
| `effectImportSource` | module specifier | `"effect"` | `jsxImportSource` |

`"transform"` desugars to library calls and auto-imports from `effectImportSource`.
`"preserve"` keeps EffectScript syntax in the output (for downstream tooling).
`"none"` makes EffectScript syntax an error (the extensions parse as plain TS).

### 1.2 Auto-import

The desugarer references the namespaces `Effect`, `Layer`, `Context`, `Scope`,
`Fiber`, `Match`, and the function `pipe`. For each one actually used by the desugared output
of a file, a namespace/named import from `effectImportSource` is synthesized unless
the file already imports that name (in which case the user's binding is used — same
rule as the classic-runtime JSX factory).

---

## 2. Lexical additions

### 2.1 New token: `<-` (BindArrow)

A single two-character token, scanned greedily **only** inside effect bodies and in
the binding positions defined in §4. Outside those positions `<-` continues to lex as
`<` `-` (so `a < -b` in plain expressions is unchanged; inside an effect body write
`a < -b` with whitespace — the no-whitespace form `a<-b` at expression level is
re-scanned as BindArrow only where a bind is grammatically possible, i.e. never in a
relational-expression position).

Precedent: the scanner already re-tokenizes `>>` vs `>`+`>` based on context.

### 2.2 Contextual keywords

`effect`, `raise`, `service`, `layer`, `fork`, `par`, `race`, `defer`, `release`,
`raises`, `requires`, `provide`, `scoped`, `join`, `match` (and the noise words
`value` / `tag` immediately after `match`).

None become reserved words. Each is recognized only in the specific grammatical
positions defined below (like `async`, `satisfies`, `accessor`). Look-ahead rules:

* `effect` is a keyword when followed (no line terminator) by an identifier and `(`
  at declaration position, by `(` or `{` at expression position, or by `*`? — no:
  there is no `effect*`.
* `raise`, `defer`, `fork`, `par`, `race`, `join` are keywords **only at the start of
  a statement or unary-expression position inside an effect body**. Code inside an
  effect body cannot call user functions with these names in those positions
  (workaround: parenthesize, e.g. `(raise)(x)`); outside effect bodies they are
  ordinary identifiers.
* `service`, `layer` are keywords at declaration position when followed by an
  identifier (same scheme as `type`, `namespace`).
* `raises`, `requires` are keywords only inside an effect-signature return clause
  (§3.2). `release` only after the operand of a `using`-bind (§8). `scoped` only as
  a modifier of `layer`/`effect`. `provide` only as an infix clause of `layer`
  declarations (§7).
* `match` is a keyword at expression position only for the exact shape
  `match [value|tag] ( Expression ) {` — i.e. the parser commits after seeing `{`
  following the closing paren (speculative parse otherwise falls back to a call of
  an identifier named `match`). Member access (`s.match(...)`) is never affected.

### 2.3 ASI

No line terminator is permitted between `effect` and what follows it, between `raise`
and its operand, or between `fork` / `join` / `par` / `race` and their operands.

---

## 3. Effect declarations and effect blocks

An **effect body** is the block of any construct in this section. The constructs of
§4–§6 and §8–§9 (binds, `raise`, `catch` arms, resources, concurrency) are legal
*only* inside effect bodies; using them elsewhere is a compile error
(error code range 18100–18199, see §13).

### 3.1 `effect` function declaration

```ts
effect hello(name: string): string raises HttpError requires Greeter {
  ...
}
```

* Desugars to `const hello = Effect.fn("hello")(function* (name: string) { ... })`
  — `Effect.fn` provides the span name `"hello"` for tracing, exactly like the
  hand-written idiom.
* The declaration is hoisted like `const` (TDZ applies), is block-scoped, and may be
  exported: `export effect hello(...) {...}` / `export default effect hello(...) {...}`.
* Generics, parameter destructuring, defaults and rest parameters are unchanged TS.
* `this` is not bound (the underlying generator's `this` is not part of the spec).

### 3.2 Effect signature sugar (`raises` / `requires`)

The return type annotation of an `effect` declaration accepts either form:

1. A plain TS type, which must be assignable to `Effect.Effect<any, any, any>`:
   `effect f(): Effect.Effect<number, Boom, Db> { ... }`
2. Sugar: `: A raises E requires R` where `raises E` and `requires R` are each
   optional. This is **type sugar** equal to `Effect.Effect<A, E, R>` (omitted
   clauses become `never`). It is also accepted in type positions inside `.ets`
   files via the type alias form `type T = A raises E requires R`.

If the annotation is omitted, all three channels are inferred (this falls out of
checking the desugared generator).

### 3.3 `effect` expressions

```ts
const f = effect (n: number) { ... };       // anonymous effect function
const program = effect { ... };             // effect block (do-notation)
```

* `effect (params) { body }` → `Effect.fn(function* (params) { body' })`.
* `effect { body }` → `Effect.gen(function* () { body' })`. The block form is an
  *expression* producing `Effect.Effect<A, E, R>`; `return` inside it sets `A`.
* Disambiguation: at expression position, `effect` followed by `(` or `{` (no line
  terminator) is always the keyword.

### 3.4 `effect` class methods

```ts
class UserRepo {
  effect getById(id: UserId): User raises NotFound requires Db { ... }
}
```

Desugars to a method whose body is `return Effect.fn("UserRepo.getById")(function* ...)(...)` —
normatively: the method becomes a field initialized with the `Effect.fn` value (so
`this` capture follows class-field semantics; see TRANSPILATION §1.4).

### 3.5 Decorator combinators on `effect` declarations

Standard decorator syntax applied to an `effect` declaration composes pipe-style
combinators in source order (top decorator outermost):

```ts
@retry(Schedule.exponential("100 millis"))
@timeout("5 seconds")
effect fetchUser(id: string): User raises FetchError { ... }
```

→ `Effect.fn("fetchUser", Effect.retry(Schedule...), Effect.timeout("5 seconds"))(function* ...)`.

The decorator expression must evaluate to `(e: Effect<...>) => Effect<...>`; the
well-known names `retry`, `timeout`, `withSpan`, `uninterruptible`, `interruptible`,
`annotateLogs`, `tapError`, `provide` resolve to the corresponding `Effect.*`
combinator when not otherwise in scope. Any in-scope user function of the right shape
is also allowed.

---

## 4. Bind: `<-`

The fundamental operator. Runs an effect and binds its success value;
failures/requirements propagate to the enclosing effect's `E`/`R` channels.

A bind **is itself the declaration** — no `const`/`let` prefix. `x <- e` introduces
a fresh, immutable, block-scoped binding (exactly `const` semantics: TDZ,
no reassignment, shadowing allowed in inner blocks).

| Form | Desugaring |
| --- | --- |
| `x <- e;` | `const x = yield* e;` |
| `{ a, b } <- e;` / `[a] <- e;` | `const { a, b } = yield* e;` etc. |
| `x: T <- e;` | `const x: T = yield* e;` |
| `<- e;` (discard statement) | `yield* e;` |
| `(<- e)` (bind expression) | `(yield* e)` |

* There are **no mutable binds**. For mutation, declare `let y` and assign with a
  bind expression: `y = (<- e);`.
* The bind expression form `(<- e)` **requires** the parentheses; this keeps the
  grammar simple: `const sum = (<- getA) + (<- getB);`
* Binding a `Context.Tag` class accesses a service: `db <- Database;`
* Disambiguation inside effect bodies: a statement starting with
  *BindingTarget* `<-` is always a bind (the parser scans ahead to the `<-` past a
  pattern/type annotation, like arrow-function lookahead). The legacy reading
  `x < -e` (less-than, negate) requires whitespace: `x < -e`. Statement-initial
  `{p} <- e` is a destructuring bind, not a block (decided by the same lookahead).

## 5. Failure: `raise`

```ts
raise new HttpError({ status: 404 });        // statement
const x = cond ? value : raise new Boom();   // expression (never type)
```

* Statement: `raise e;` → `return yield* Effect.fail(e);`
* Expression: `raise e` → `(yield* Effect.fail(e))`, which has type `never`.
* `raise.die e;` → `Effect.die(e)` (defects). `raise` alone covers the typed error
  channel.

## 6. Error handling: postfix `catch` arms

Error handling attaches **directly to the effect being run**, as a postfix `catch`
block with match-style arms (`>>`):

```ts
res <- http.get(`/quote/${symbol}`) catch {
  NotFound          >> Quote.empty
  RateLimited as e  >> { <- Effect.sleep(e.retryAfter); raise e }
  DbError | NetError as e >> raise new HttpError({ status: 503, cause: e })
  _ as e            >> { <- Effect.logError(e); raise e }
}
```

* `expr catch { arms }` is a postfix expression form, valid inside effect bodies on
  any effect-typed expression. It desugars to `expr.pipe(...)` with one
  `Effect.catchTag` / `Effect.catchTags` / `Effect.catchAll` per arm
  (normative rules in TRANSPILATION §4).
* Arm shape: `Tag₁ | Tag₂ … [as binding] >> handler` where `handler` is an
  expression or a `{ ... }` effect body. An expression handler is the recovery
  value (effectful sub-expressions like `raise` and `(<- e)` are allowed inside it).
* `_ [as binding]` is the catch-all arm (→ `Effect.catchAll`) and must be last.
* A tag reference must name a class type with a string-literal `_tag`
  (i.e. `Data.TaggedError` / `Schema.TaggedError` style). Anything else is error
  18110 — use the `_` arm for the untyped case.
* To guard a *region* rather than a single call, attach `catch` to an effect block:

```ts
page <- effect {
  user <- fetchUser(id)
  return render(user)
} catch {
  NotFound >> render404(id)
}
```

* Plain JS `try/catch` inside an effect body keeps its standard meaning — it does
  **not** intercept Effect failures; a warning (18111) points to `catch` arms.
* Finalization is orthogonal: use `defer { ... }` (§8) or the `@ensuring(...)`
  decorator; there is no `finally` arm.

## 7. Services and layers

### 7.1 `service` declaration

```ts
service Database {
  query(sql: string): Effect.Effect<Rows, DbError>
  readonly url: string
}
```

→

```ts
class Database extends Context.Tag("Database")<Database, {
  query(sql: string): Effect.Effect<Rows, DbError>;
  readonly url: string;
}>() {}
```

* The tag string is the declared name, prefixed by the value of
  `@effectServicePrefix` (a per-file pragma comment, default empty) for uniqueness.
* `export service X { ... }` exports the class.
* A service is used by binding it: `db <- Database`.

### 7.2 `layer` declaration

```ts
layer DatabaseLive: Database {
  cfg  <- Config
  pool <- PgPool
  return {
    query: (sql) => pool.query(sql),
    url: cfg.dbUrl,
  }
}
```

→ `const DatabaseLive = Layer.effect(Database, Effect.gen(function* () { ... }))`

Modifiers and clauses:

| Form | Desugaring |
| --- | --- |
| `layer X: Tag { body }` | `Layer.effect(Tag, Effect.gen(...))` |
| `scoped layer X: Tag { body }` | `Layer.scoped(Tag, Effect.gen(...))` |
| `layer X: Tag = expr` | `Layer.succeed(Tag, expr)` |
| `layer X provide [A, B] { ... }`* | wraps result in `Layer.provide([A, B])` |

\* `provide` lists dependency layers baked into this layer:
`const X = Layer.effect(Tag, ...).pipe(Layer.provide([A, B]))`.

### 7.3 Providing at the edge

No dedicated statement — use the pipeline operator (§10):
`program |> Effect.provide(MainLive) |> Effect.runPromise`.

## 8. Resources and finalization

```ts
using conn <- acquireConn(cfg) release (c) { <- c.close() }
```

→ `const conn = yield* Effect.acquireRelease(acquireConn(cfg), (c) => Effect.gen(function* () { yield* c.close(); }))`

* The `release` clause body is an effect body. Its parameter list also accepts the
  exit: `release (c, exit) { ... }`.
* `using x <- e` *without* `release` requires `e: Effect<..., ..., Scope | ...>`'s
  value or simply binds a scoped effect: it desugars to plain `yield*` and exists for
  intent-signaling; the enclosing effect must provide the `Scope` (e.g. `scoped
  layer`, or piping through `Effect.scoped`).
* `defer { body }` → `yield* Effect.addFinalizer((exit) => Effect.gen(function* () { body' }))`.
  `defer (exit) { body }` binds the exit value.

## 9. Concurrency

All forms are unary operators / expressions legal inside effect bodies.

| Syntax | Desugaring | Notes |
| --- | --- | --- |
| `fork e` | `Effect.fork(e)` | usually bound: `fiber <- fork e` |
| `join f` | `Fiber.join(f)` | `x <- join fiber` |
| `par [e1, e2, ...]` | `Effect.all([e1, e2, ...], { concurrency: "unbounded" })` | tuple result |
| `par { a: e1, b: e2 }` | `Effect.all({ ... }, { concurrency: "unbounded" })` | struct result |
| `par(n) [...]` / `par(n) {...}` | `{ concurrency: n }` | bounded |
| `race [e1, e2, ...]` | `Effect.race(e1, Effect.race(e2, ...))` / `Effect.raceAll` | first winner |

`par` and `race` produce effects; bind them to get values:
`[a, b] <- par [getA, getB]`.

## 10. Pipeline operator `|>`

Available anywhere in `.ets`/`.etsx` (not just effect bodies), F#-style:

```
a |> f          ≡ f(a)
a |> f(b, c)    ≡ f(b, c)(a)        // data-last application
a |> f |> g     ≡ g(f(a))
```

* Precedence: lower than `??`, higher than ternary; left-associative.
* This matches Effect's data-last, curried combinator design:
  `program |> Effect.retry(policy) |> Effect.provide(Main) |> Effect.runPromise`.
* `a |> f(b)` always means `f(b)(a)`. To pipe into a direct call use an arrow:
  `a |> (x => f(x, b))`.

## 11. Pattern matching: `match`

Rust-style match **expression**, desugaring to `effect/Match`. Two scrutinee modes:

### 11.1 Value mode — `match (x)` (alias: `match value (x)`)

Arms test structure/literals; capitalized identifiers are tag references,
lowercase identifiers are bindings (Rust convention, enforced by 18150):

```ts
const label = match (res) {
  { status: 200, body }        >> body
  { status: 301 | 302 }        >> "redirect"
  { status: s } if s >= 500    >> `server error ${s}`
  [first, ...rest]             >> first
  "timeout"                    >> "timed out"
  NotFound as e                >> e.id          // tagged-class arm
  _                            >> "unknown"
}
```

* Pattern forms: literals (`200`, `"a"`, `true`, `null`), or-patterns (`a | b`),
  object patterns (literal fields = tests, identifier fields = bindings, nested
  patterns allowed), array patterns (with rest), tagged-class references
  (`Tag [as x]`), bindings (lowercase identifier, must be last arm or guarded),
  wildcard `_ [as x]`.
* Guards: `pattern if expr >>` — guard sees the pattern's bindings.
* Without a `_`/binding arm the match must be **exhaustive** (desugars to
  `Match.exhaustive`; non-exhaustiveness is a type error). With one, it desugars to
  `Match.orElse`.

### 11.2 Tag mode — `match tag (x)`

Every arm is a tag of the scrutinee's discriminated union (`_tag`); exhaustiveness
is over the union's tags:

```ts
const msg = match tag (error) {
  NotFound as e   >> `missing ${e.id}`
  DbError | NetError >> "infra down"
  _               >> "unexpected"
}
```

### 11.3 Effectful matches

Outside effect bodies a `match` is a pure expression (arms may not use `<-`,
`raise`, etc.). Inside an effect body, arms are effect bodies — expression arms may
contain `raise`/`(<- e)` and block arms (`>> { ... }`) are full effect bodies; the
whole match participates in the enclosing effect (TRANSPILATION §10).

## 12. JSX interoperability

`.etsx` files combine both extension sets. JSX parsing is unchanged. Effect blocks
may appear in JSX expression containers and vice versa:

```tsx
const Page = () => {
  const run = useRunEffect();
  return <button onClick={() => run(effect {
    user <- fetchUser(id)
    <- Telemetry.click("buy")
    return user
  })}>Buy</button>;
};
```

Grammar note: a statement-initial `<-` (discard bind) is unambiguous even in `.etsx`
because a JSX element's `<` must be followed by an identifier, `>`, or `/`.

## 13. Diagnostics (new range 18100–18199)

| Code | Message (sketch) |
| --- | --- |
| 18100 | `'<-' bind is only allowed inside an effect body.` |
| 18101 | `'raise' is only allowed inside an effect body.` |
| 18102 | `'effect' declarations require a body.` |
| 18103 | `Operand of '<-' must be an Effect.` (surfaced from checker on the desugared `yield*`) |
| 18110 | `Catch arm tag must be a tagged error class (a class with a string-literal '_tag').` |
| 18111 | `'try' inside an effect body does not catch Effect failures; attach 'catch { ... }' arms to the effect instead.` (warning) |
| 18112 | `'break'/'continue' cannot cross an 'effect' block boundary.` |
| 18113 | `'catch' arms can only be attached to an Effect-typed expression.` |
| 18120 | `'par'/'race'/'fork'/'join' are only allowed inside an effect body.` |
| 18130 | `'using ... <-' requires a Scope in context; add 'scoped' or provide one.` |
| 18140 | `Decorator on an 'effect' declaration must be an Effect combinator.` |
| 18150 | `Pattern identifiers must be lowercase bindings or capitalized tag references.` |
| 18151 | `Unreachable match arm (follows a catch-all arm).` |
| 18152 | `'match' is not exhaustive; add the missing arms or a '_' arm.` (surfaced via Match.exhaustive) |
| 18153 | `Effectful match arms ('<-', 'raise') are only allowed inside an effect body.` |

## 14. Semantics guarantee

Every construct's meaning is **defined as** the meaning of its desugaring in
TRANSPILATION.md against the public `effect` API. There is no independent runtime
semantics; an EffectScript program and its transpilation are observationally
identical by construction.

## 15. Out of scope for v1 (future work)

* `Stream`/`Sink` comprehension syntax (`for await`-like sugar).
* STM blocks (`atomic { ... }` → `STM.gen`).
* Schema literal types.
* Top-level `main` runner sugar.
