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

export const program = effect {
  body <- fetchPage("/home") catch {
    NotFound as e >> e.id
    Forbidden >> "forbidden"
    RateLimited as e >> {
      <- Effect.sleep(e.retryAfter);
      raise e;
    }
    DbError | NetError as e >> {
      <- Effect.logError(e);
      return "infra";
    }
    _ as e >> {
      <- Effect.logError(e);
      return "fallback";
    }
  };
  return body.length;
};
