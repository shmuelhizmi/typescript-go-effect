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

export const program = effect {
  res <- http.get("/quote/AAPL") catch {
    NotFound >> ""
    RateLimited as e >> {
      <- Effect.sleep(e.retryAfter);
      raise e;
    }
    DbError | NetError as e >> (raise new NetError())
    _ as e >> {
      <- Effect.logError(e);
      return "fallback";
    }
  };

  fiber <- fork ea;
  joined <- join fiber;
  [a, b] <- par [ea, eb];
  named <- par { first: ea, second: eb };
  bounded <- par(2) [ea, ea, ea];
  winner <- race [ea, ea];
  fastest <- race [ea, ea, ea];

  return res.length + joined + a + b.length + named.first + bounded.length + winner + fastest;
};
