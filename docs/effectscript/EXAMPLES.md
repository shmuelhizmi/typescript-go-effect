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
  const repo <- UserRepo
  const user <- repo.findById(id)
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

## 2. Typed error handling

```ts
effect loadPage(id: string): string {
  const html = try {
    const user <- getUser(id)
    return render(user)
  } catch (e: NotFound) {
    return render404(e.id)
  } catch (e: DbError | NetError) {
    raise new HttpError({ status: 503, cause: e })
  } finally {
    <- Effect.logDebug("loadPage finished")
  }
  return html
}
```

⇣

```ts
const loadPage = Effect.fn("loadPage")(function* (id: string) {
  const html = yield* Effect.gen(function* () {
    const user = yield* getUser(id);
    return render(user);
  }).pipe(
    Effect.catchTag("NotFound", (e) => Effect.gen(function* () {
      return render404(e.id);
    })),
    Effect.catchTags({
      DbError: (e) => Effect.gen(function* () {
        return yield* Effect.fail(new HttpError({ status: 503, cause: e }));
      }),
      NetError: (e) => Effect.gen(function* () {
        return yield* Effect.fail(new HttpError({ status: 503, cause: e }));
      }),
    }),
    Effect.ensuring(Effect.gen(function* () {
      yield* Effect.logDebug("loadPage finished");
    })),
  );
  return html;
});
```

## 3. Layers and wiring

```ts
layer UserRepoLive: UserRepo provide [PgPoolLive] {
  const pool <- PgPool
  return {
    findById: (id) => pool.queryOne(`select * from users where id = $1`, [id]),
  }
}

scoped layer PgPoolLive: PgPool {
  using pool <- createPool(cfg) release (p) { <- p.end() }
  return pool
}

effect main(): void requires UserRepo {
  const user <- getUser("42")
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

## 4. Concurrency

```ts
effect dashboard(userId: string): Dashboard {
  const [user, orders, recs] <- par [
    getUser(userId),
    getOrders(userId),
    getRecommendations(userId),
  ]

  const refresher <- fork pollUpdates(userId)
  defer { <- Fiber.interrupt(refresher) }

  const fastest <- race [cdnFetch(user.avatar), originFetch(user.avatar)]
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

## 5. Decorator combinators

```ts
@retry(Schedule.exponential("100 millis", 2).pipe(Schedule.upTo("5 seconds")))
@timeout("10 seconds")
effect fetchQuote(symbol: string): Quote raises QuoteError requires Http {
  const http <- Http
  const res  <- http.get(`/quote/${symbol}`)
  return parseQuote((<- res.json))
}
```

⇣

```ts
const fetchQuote = Effect.fn(
  "fetchQuote",
  Effect.timeout("10 seconds"),
  Effect.retry(Schedule.exponential("100 millis", 2).pipe(Schedule.upTo("5 seconds"))),
)(function* (symbol: string) {
  const http = yield* Http;
  const res = yield* http.get(`/quote/${symbol}`);
  return parseQuote((yield* res.json));
});
```

## 6. React interop (`.etsx`)

EffectScript relates to Effect exactly as JSX relates to React — and the two
compose in one file:

```tsx
// BuyButton.etsx
effect purchase(item: ItemId): Receipt raises PaymentError requires Payments {
  const pay <- Payments
  const receipt <- pay.charge(item)
  <- Analytics.track("purchase", { item })
  return receipt
}

export function BuyButton({ item }: { item: ItemId }) {
  const run = useEffectRunner();      // app-level runtime hook
  return (
    <button onClick={() => run(effect {
      const receipt <- purchase(item)
      <- Effect.log(`charged ${receipt.amount}`)
    })}>
      Buy now
    </button>
  );
}
```

The JSX desugars through the standard JSX transform; the `effect` constructs
through the EffectScript transform. Two orthogonal sugars, one file.

## 7. Expression-level binds

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
