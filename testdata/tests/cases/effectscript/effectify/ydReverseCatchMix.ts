// Deeper catch chain: several distinct catchTag arms, a shared-handler
// catchTags arm, and a trailing catchAll — all in one gen, reversing to a
// multi-arm `catch { }`.
import { Effect } from "effect";

class NotFound {
  readonly _tag = "NotFound";
  readonly id = "";
}
class Forbidden {
  readonly _tag = "Forbidden";
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

declare const fetchPage: (url: string) => Effect.Effect<string, NotFound | Forbidden | RateLimited | DbError | NetError>;

export const program = Effect.gen(function* () {
  const body = yield* fetchPage("/home").pipe(
    Effect.catchTag("NotFound", (e) => Effect.gen(function* () {
      return e.id;
    })),
    Effect.catchTag("Forbidden", (_) => Effect.gen(function* () {
      return "forbidden";
    })),
    Effect.catchTag("RateLimited", (e) => Effect.gen(function* () {
      yield* Effect.sleep(e.retryAfter);
      return yield* Effect.fail(e);
    })),
    ((h) => Effect.catchTags({ DbError: h, NetError: h }))((e) => Effect.gen(function* () {
      yield* Effect.logError(e);
      return "infra";
    })),
    Effect.catchAll((e) => Effect.gen(function* () {
      yield* Effect.logError(e);
      return "fallback";
    })),
  );
  return body.length;
});
