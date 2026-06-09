# Effect Feature Coverage Matrix

How every major area of the `effect` library is expressed in EffectScript.
"Idiom" means the feature needs no dedicated syntax — the general constructs
(`<-`, `|>`, `effect {}`) already make it ergonomic.

| Effect feature | Library API | EffectScript | Status |
| --- | --- | --- | --- |
| Do-notation / sequencing | `Effect.gen` + `yield*` | `effect { }` blocks, `x <- e` binds (no `const` needed, always immutable) | ✅ syntax |
| Named, traced functions | `Effect.fn("name")` | `effect name() {}` declarations | ✅ syntax |
| Anonymous effect fns | `Effect.fn` | `effect (x) {}` expressions | ✅ syntax |
| Success value | `return` in gen | `return` | ✅ inherited |
| Typed failures | `Effect.fail` | `raise e` | ✅ syntax |
| Defects | `Effect.die` | `raise.die e` | ✅ syntax |
| Catch by tag | `Effect.catchTag` | `e catch { NotFound [as x] >> … }` | ✅ syntax |
| Catch multiple tags | `Effect.catchTags` | `e catch { A \| B as x >> … }` | ✅ syntax |
| Catch all | `Effect.catchAll` | `e catch { _ as x >> … }` | ✅ syntax |
| Finalization | `Effect.ensuring` / `addFinalizer` | `defer { }` / `@ensuring(...)` | ✅ syntax |
| Effect type | `Effect.Effect<A, E, R>` | `A raises E requires R` type sugar | ✅ syntax |
| Services (Tag) | `Context.Tag` | `service Name { }` | ✅ syntax |
| Service access | `yield* Tag` | `const svc <- Tag` | ✅ syntax |
| Layers (effectful) | `Layer.effect` | `layer L: Tag { }` | ✅ syntax |
| Layers (scoped) | `Layer.scoped` | `scoped layer L: Tag { }` | ✅ syntax |
| Layers (value) | `Layer.succeed` | `layer L: Tag = expr` | ✅ syntax |
| Layer composition | `Layer.provide` | `layer … provide [A, B]` | ✅ syntax |
| Providing at edge | `Effect.provide` | `program \|> Effect.provide(L)` | ✅ idiom (`\|>`) |
| Pipelines | `pipe(...)` | `\|>` operator | ✅ syntax |
| Fork fibers | `Effect.fork` | `fork e` | ✅ syntax |
| Join fibers | `Fiber.join` | `join f` | ✅ syntax |
| Structured concurrency | `Effect.all` + concurrency | `par [..]` / `par {..}` / `par(n)` | ✅ syntax |
| Racing | `Effect.race` / `raceAll` | `race [a, b, …]` | ✅ syntax |
| Resource acquire/release | `Effect.acquireRelease` | `using x <- acq release (c) { }` | ✅ syntax |
| Scoped binds | `Scope` | `using x <- scopedEffect` | ✅ syntax |
| Finalizers | `Effect.addFinalizer` | `defer { }` / `defer (exit) { }` | ✅ syntax |
| Retry | `Effect.retry` | `@retry(schedule)` decorator | ✅ syntax |
| Timeout | `Effect.timeout` | `@timeout(dur)` decorator | ✅ syntax |
| Tracing spans | `Effect.withSpan` | `effect name` auto-span; `@withSpan` | ✅ syntax |
| Interruption control | `Effect.uninterruptible` | `@uninterruptible` decorator | ✅ syntax |
| Repeat / schedules | `Effect.repeat`, `Schedule.*` | `e \|> Effect.repeat(sched)` | ✅ idiom |
| Logging | `Effect.log*` | `<- Effect.logInfo(msg)` | ✅ idiom |
| Config | `Config.*` | `const port <- Config.number("PORT")` | ✅ idiom |
| Ref / state | `Ref.*` | `const r <- Ref.make(0)` | ✅ idiom |
| Deferred / Queue / PubSub | `Deferred.*` etc. | binds + `\|>` | ✅ idiom |
| Running (edge) | `Effect.runPromise`, `runMain` | `main() \|> NodeRuntime.runMain` | ✅ idiom |
| Option / Either / Data | data modules | plain TS (no sugar needed) | ✅ idiom |
| Pattern matching (value) | `Match.value` + `when`/`whenOr` | `match (x) { pattern >> … }` | ✅ syntax |
| Pattern matching (tags) | `Match.tag` / `Match.tags` | `match tag (x) { Tag >> … }` | ✅ syntax |
| Match exhaustiveness | `Match.exhaustive` / `orElse` | no `_` arm → exhaustive; `_` arm → fallback | ✅ syntax |
| Streams | `Stream.*` | binds + `\|>`; comprehensions | 🔮 future |
| STM | `STM.gen` | `atomic { }` | 🔮 future |
| Schema | `effect/Schema` | literal type syntax | 🔮 future |

**Design test applied throughout:** a feature only gets dedicated syntax when the
library forces either generator boilerplate (`function*`/`yield*`), class
boilerplate (`Context.Tag` dance), or deeply nested combinators (`catchTags`
handler objects). Everything else stays a library call — keeping the language small
and the output predictable.
