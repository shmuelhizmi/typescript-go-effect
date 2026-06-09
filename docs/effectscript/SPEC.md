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
`Fiber`, and the function `pipe`. For each one actually used by the desugared output
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
`raises`, `requires`, `provide`, `scoped`, `join`.

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

### 2.3 ASI

No line terminator is permitted between `effect` and what follows it, between `raise`
and its operand, or between `fork` / `join` / `par` / `race` and their operands.

---

## 3. Effect declarations and effect blocks

An **effect body** is the block of any construct in this section. The constructs of
§4–§9 are legal *only* inside effect bodies; using them elsewhere is a compile error
(error code range 18100–18199, see §12).

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

| Form | Desugaring |
| --- | --- |
| `const x <- e;` | `const x = yield* e;` |
| `let x <- e;` | `let x = yield* e;` |
| `const { a, b } <- e;` / `const [a] <- e;` | `const { a, b } = yield* e;` etc. |
| `const x: T <- e;` | `const x: T = yield* e;` |
| `<- e;` (discard statement) | `yield* e;` |
| `(<- e)` (bind expression) | `(yield* e)` |

* The bind expression form `(<- e)` **requires** the parentheses; this keeps the
  grammar LL(1)-friendly and reads like Haskell's desugared `<-`:
  `const sum = (<- getA) + (<- getB);`
* Multiple declarators may mix forms: `const a <- ea, b = 3;` is legal.
* Binding a `Context.Tag` class accesses a service: `const db <- Database;`
* `var x <- e` is **not** legal (no hoisted binds).

## 5. Failure: `raise`

```ts
raise new HttpError({ status: 404 });        // statement
const x = cond ? value : raise new Boom();   // expression (never type)
```

* Statement: `raise e;` → `return yield* Effect.fail(e);`
* Expression: `raise e` → `(yield* Effect.fail(e))`, which has type `never`.
* `raise.die e;` → `Effect.die(e)` (defects). `raise` alone covers the typed error
  channel.

## 6. Error handling: typed `try` / `catch`

EffectScript lifts TypeScript's restriction that a catch-clause annotation must be
`any`/`unknown`, allows **multiple catch clauses**, and gives them Effect semantics:

```ts
try {
  const user <- fetchUser(id)
  return render(user)
} catch (e: NotFound) {            // tagged-error class → Effect.catchTag
  return renderMissing(id)
} catch (e: DbError | NetError) {  // union of tagged errors → Effect.catchTags
  raise new HttpError({ cause: e })
} catch (e) {                      // bare → Effect.catchAll
  return renderOops(e)
} finally {                        // → Effect.ensuring
  <- Metrics.increment("requests")
}
```

* The `try` block and each handler block are effect bodies; each desugars to an
  `Effect.gen` wrapped with `catchTag` / `catchTags` / `catchAll` / `ensuring`
  (normative rules in TRANSPILATION §4).
* A catch annotation must be (a union of) class types with a string-literal `_tag`
  (i.e. `Data.TaggedError` / `Schema.TaggedError` style). Anything else is error
  18110 — use a bare `catch (e)` for the untyped case.
* The whole `try` is an *expression-statement-like* construct whose value
  participates in the enclosing effect: `const r = try { ... } catch (...) { ... }`
  is permitted (try-expression), desugaring to a bound `yield*`.
* Plain JS `try/catch` (no typed clauses, single catch) inside an effect body keeps
  its standard meaning — it does **not** intercept Effect failures, and a warning
  (18111) nudges toward typed catch.

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
* A service is used by binding it: `const db <- Database`.

### 7.2 `layer` declaration

```ts
layer DatabaseLive: Database {
  const cfg  <- Config
  const pool <- PgPool
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
| `fork e` | `Effect.fork(e)` | usually bound: `const fiber <- fork e` |
| `join f` | `Fiber.join(f)` | `const x <- join fiber` |
| `par [e1, e2, ...]` | `Effect.all([e1, e2, ...], { concurrency: "unbounded" })` | tuple result |
| `par { a: e1, b: e2 }` | `Effect.all({ ... }, { concurrency: "unbounded" })` | struct result |
| `par(n) [...]` / `par(n) {...}` | `{ concurrency: n }` | bounded |
| `race [e1, e2, ...]` | `Effect.race(e1, Effect.race(e2, ...))` / `Effect.raceAll` | first winner |

`par` and `race` produce effects; bind them to get values:
`const [a, b] <- par [getA, getB]`.

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

## 11. JSX interoperability

`.etsx` files combine both extension sets. JSX parsing is unchanged. Effect blocks
may appear in JSX expression containers and vice versa:

```tsx
const Page = () => {
  const run = useRunEffect();
  return <button onClick={() => run(effect {
    const user <- fetchUser(id)
    <- Telemetry.click("buy")
    return user
  })}>Buy</button>;
};
```

Grammar note: a statement-initial `<-` (discard bind) is unambiguous even in `.etsx`
because a JSX element's `<` must be followed by an identifier, `>`, or `/`.

## 12. Diagnostics (new range 18100–18199)

| Code | Message (sketch) |
| --- | --- |
| 18100 | `'<-' bind is only allowed inside an effect body.` |
| 18101 | `'raise' is only allowed inside an effect body.` |
| 18102 | `'effect' declarations require a body.` |
| 18103 | `Operand of '<-' must be an Effect.` (surfaced from checker on the desugared `yield*`) |
| 18110 | `Catch clause type must be a tagged error class or union of them.` |
| 18111 | `Untyped 'try' inside an effect body does not catch Effect failures.` (warning) |
| 18120 | `'par'/'race'/'fork'/'join' are only allowed inside an effect body.` |
| 18130 | `'using ... <-' requires a Scope in context; add 'scoped' or provide one.` |
| 18140 | `Decorator on an 'effect' declaration must be an Effect combinator.` |

## 13. Semantics guarantee

Every construct's meaning is **defined as** the meaning of its desugaring in
TRANSPILATION.md against the public `effect` API. There is no independent runtime
semantics; an EffectScript program and its transpilation are observationally
identical by construction.

## 14. Out of scope for v1 (future work)

* `Stream`/`Sink` comprehension syntax (`for await`-like sugar).
* `match` expression sugar over `effect/Match`.
* STM blocks (`atomic { ... }` → `STM.gen`).
* Schema literal types.
* Top-level `main` runner sugar.
