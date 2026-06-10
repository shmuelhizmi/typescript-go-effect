// postfix catch arms and fork/join/par/race
import { Effect, Fiber } from "effect";

class NotFound {
  readonly _tag = "NotFound";
  readonly id = "";
}
class RateLimited {
  readonly _tag = "RateLimited";
  readonly retryAfter = 100;
}
class DbError {
  readonly _tag = "DbError";
}
class NetError {
  readonly _tag = "NetError";
}

declare const http: { get: (url: string) => Effect.Effect<string, NotFound | RateLimited | DbError | NetError> };
declare const ea: Effect.Effect<number>;
declare const eb: Effect.Effect<string>;

export const program = Effect.gen(function* () {
  const res = yield* http.get("/quote/AAPL").pipe(
    Effect.catchTag("NotFound", (_) => Effect.gen(function* () {
      return "";
    })),
    Effect.catchTag("RateLimited", (e) => Effect.gen(function* () {
      yield* Effect.sleep(e.retryAfter);
      return yield* Effect.fail(e);
    })),
    ((h) => Effect.catchTags({ DbError: h, NetError: h }))((e) => Effect.gen(function* () {
      return yield* Effect.fail(new NetError());
    })),
    Effect.catchAll((e) => Effect.gen(function* () {
      yield* Effect.logError(e);
      return "fallback";
    })),
  );

  const fiber = yield* Effect.fork(ea);
  const joined = yield* Fiber.join(fiber);
  const [a, b] = yield* Effect.all([ea, eb], { concurrency: "unbounded" });
  const named = yield* Effect.all({ first: ea, second: eb }, { concurrency: "unbounded" });
  const bounded = yield* Effect.all([ea, ea, ea], { concurrency: 2 });
  const winner = yield* Effect.race(ea, ea);
  const fastest = yield* Effect.raceAll([ea, ea, ea]);

  return res.length + joined + a + b.length + named.first + bounded.length + winner + fastest;
});
