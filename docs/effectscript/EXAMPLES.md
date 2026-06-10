# EffectScript by Example

Each example shows EffectScript source and its exact transpilation.

---

## 1. Hello, errors and services

```ts
// users.ets
import { Data } from "effect";

class NotFound extends Data.TaggedError("NotFound")<{ id: string }> {}

service UserRepo {
  findById(id: string): User | undefined raises DbError
}

effect getUser(id: string): User raises NotFound | DbError requires UserRepo {
  repo <- UserRepo
  user <- repo.findById(id)
  if (user === undefined) raise new NotFound({ id })
  return user
}
```

⇣ transpiles to

```ts
import { Data } from "effect";
import { Effect, Context } from "effect";

class NotFound extends Data.TaggedError("NotFound")<{ id: string }> {}

class UserRepo extends Context.Tag("UserRepo")<UserRepo, {
  findById(id: string): Effect.Effect<User | undefined, DbError>;
}>() {}

const getUser = Effect.fn("getUser")(function* (id: string) {
  const repo = yield* UserRepo;
  const user = yield* repo.findById(id);
  if (user === undefined) return yield* Effect.fail(new NotFound({ id }));
  return user;
});
```

## 2. Postfix `catch` arms

Handlers attach to the effect they guard:

```ts
effect loadPage(id: string): string {
  html <- renderUser(id) catch {
    NotFound as e            >> render404(e.id)
    DbError | NetError as e  >> raise new HttpError({ status: 503, cause: e })
    _ as e                   >> { <- Effect.logError(e); return "oops" }
  }
  return html
}
```

⇣

```ts
const loadPage = Effect.fn("loadPage")(function* (id: string) {
  const html = yield* renderUser(id).pipe(
    Effect.catchTag("NotFound", (e) => Effect.succeed(render404(e.id))),
    Effect.catchTags({
      DbError: (e) => Effect.fail(new HttpError({ status: 503, cause: e })),
      NetError: (e) => Effect.fail(new HttpError({ status: 503, cause: e })),
    }),
    Effect.catchAll((e) => Effect.gen(function* () {
      yield* Effect.logError(e);
      return "oops";
    })),
  );
  return html;
});
```

To guard a multi-statement region, attach `catch` to an `effect { }` block:

```ts
page <- effect {
  user <- fetchUser(id)
  return render(user)
} catch {
  NotFound >> render404(id)
}
```

## 3. Rust-style `match`

Value mode and tag mode:

```ts
const label = match (res) {
  { status: 200, body }      >> body
  { status: 301 | 302 }      >> "redirect"
  { status: s } if s >= 500  >> `server error ${s}`
  _                          >> "unknown"
}

const msg = match tag (error) {
  NotFound as e       >> `missing ${e.id}`
  DbError | NetError  >> "infra down"
  _                   >> "unexpected"
}
```

⇣

```ts
const label = Match.value(res).pipe(
  Match.when({ status: 200 }, ({ body }) => body),
  Match.whenOr({ status: 301 }, { status: 302 }, () => "redirect"),
  Match.when((v) => v.status >= 500, ({ status: s }) => `server error ${s}`),
  Match.orElse(() => "unknown"),
);

const msg = Match.value(error).pipe(
  Match.tag("NotFound", (e) => `missing ${e.id}`),
  Match.tags({ DbError: () => "infra down", NetError: () => "infra down" }),
  Match.orElse(() => "unexpected"),
);
```

## 4. Layers and wiring

```ts
layer UserRepoLive: UserRepo provide [PgPoolLive] {
  pool <- PgPool
  return {
    findById: (id) => pool.queryOne(`select * from users where id = $1`, [id]),
  }
}

scoped layer PgPoolLive: PgPool {
  using pool <- createPool(cfg) release (p) { <- p.end() }
  return pool
}

effect main(): void requires UserRepo {
  user <- getUser("42")
  <- Effect.log(user.name)
}

main() |> Effect.provide(UserRepoLive) |> Effect.runPromise
```

⇣

```ts
const UserRepoLive = Layer.effect(UserRepo, Effect.gen(function* () {
  const pool = yield* PgPool;
  return {
    findById: (id) => pool.queryOne(`select * from users where id = $1`, [id]),
  };
})).pipe(Layer.provide([PgPoolLive]));

const PgPoolLive = Layer.scoped(PgPool, Effect.gen(function* () {
  const pool = yield* Effect.acquireRelease(createPool(cfg), (p) =>
    Effect.gen(function* () { yield* p.end(); }));
  return pool;
}));

const main = Effect.fn("main")(function* () {
  const user = yield* getUser("42");
  yield* Effect.log(user.name);
});

pipe(main(), Effect.provide(UserRepoLive), Effect.runPromise);
```

## 5. Concurrency

```ts
effect dashboard(userId: string): Dashboard {
  [user, orders, recs] <- par [
    getUser(userId),
    getOrders(userId),
    getRecommendations(userId),
  ]

  refresher <- fork pollUpdates(userId)
  defer { <- Fiber.interrupt(refresher) }

  fastest <- race [cdnFetch(user.avatar), originFetch(user.avatar)]
  return { user, orders, recs, avatar: fastest }
}
```

⇣

```ts
const dashboard = Effect.fn("dashboard")(function* (userId: string) {
  const [user, orders, recs] = yield* Effect.all([
    getUser(userId),
    getOrders(userId),
    getRecommendations(userId),
  ], { concurrency: "unbounded" });

  const refresher = yield* Effect.fork(pollUpdates(userId));
  yield* Effect.addFinalizer(() => Effect.gen(function* () {
    yield* Fiber.interrupt(refresher);
  }));

  const fastest = yield* Effect.race(cdnFetch(user.avatar), originFetch(user.avatar));
  return { user, orders, recs, avatar: fastest };
});
```

## 6. Decorator combinators

```ts
@retry(Schedule.exponential("100 millis", 2).pipe(Schedule.upTo("5 seconds")))
@timeout("10 seconds")
effect fetchQuote(symbol: string): Quote raises QuoteError requires Http {
  http <- Http
  res  <- http.get(`/quote/${symbol}`) catch {
    RateLimited >> Quote.cached(symbol)
  }
  return parseQuote((<- res.json))
}
```

⇣

```ts
const fetchQuote = Effect.fn("fetchQuote")(
  function* (symbol: string) {
    const http = yield* Http;
    const res = yield* http.get(`/quote/${symbol}`).pipe(
      Effect.catchTag("RateLimited", () => Effect.succeed(Quote.cached(symbol))),
    );
    return parseQuote((yield* res.json));
  },
  Effect.timeout("10 seconds"),
  Effect.retry(Schedule.exponential("100 millis", 2).pipe(Schedule.upTo("5 seconds"))),
);
```

## 7. React interop (`.etsx`)

EffectScript relates to Effect exactly as JSX relates to React — and the two
compose in one file:

```tsx
// BuyButton.etsx
effect purchase(item: ItemId): Receipt raises PaymentError requires Payments {
  pay     <- Payments
  receipt <- pay.charge(item)
  <- Analytics.track("purchase", { item })
  return receipt
}

export function BuyButton({ item }: { item: ItemId }) {
  const run = useEffectRunner();      // app-level runtime hook
  return (
    <button onClick={() => run(effect {
      receipt <- purchase(item)
      <- Effect.log(`charged ${receipt.amount}`)
    })}>
      Buy now
    </button>
  );
}
```

The JSX desugars through the standard JSX transform; the `effect` constructs
through the EffectScript transform. Two orthogonal sugars, one file.

## 8. Expression-level binds

```ts
effect total(cart: Cart): number requires Pricing {
  return (<- subtotal(cart)) + (<- shipping(cart)) + (<- tax(cart))
}
```

⇣

```ts
const total = Effect.fn("total")(function* (cart: Cart) {
  return (yield* subtotal(cart)) + (yield* shipping(cart)) + (yield* tax(cart));
});
```
