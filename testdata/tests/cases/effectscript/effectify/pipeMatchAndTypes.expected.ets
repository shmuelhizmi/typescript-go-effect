// pipeline operator, match expressions and raises/requires type sugar
import { Effect, Match, pipe } from "effect";

class NotFound {
  readonly _tag = "NotFound";
  readonly id = "";
}
class DbError {
  readonly _tag = "DbError";
}
class NetError {
  readonly _tag = "NetError";
}

declare const program: Effect.Effect<number, NotFound, never>;
declare const policy: unknown;

type Fetched = string;
type LookupResult = Fetched raises NotFound requires typeof Match;

declare function fetchQuote(symbol: string): number raises NotFound | DbError;

export function lookup(id: string): Fetched raises NotFound {
  return Effect.succeed(id);
}

export const main = program |> Effect.retry(policy) |> Effect.runPromise;

export const piped = program |> Effect.retry(policy) |> Effect.withSpan("piped");

declare const res: { status: number } | string | NotFound | DbError | NetError;

export const label = match (res) {
  "timeout" >> "timed out"
  301 | 302 >> "redirect"
  NotFound as e >> e.id
  DbError | NetError >> "infra down"
  _ as other >> `unknown: ${other}`
};

declare const code: 200 | 404 | 500;

export const exhaustive = match (code) {
  200 >> "ok"
  404 >> "missing"
  500 >> "boom"
};

export const effectful = effect {
  quote <- fetchQuote("AAPL");
  const verdict = match (quote) {
    0 >> (raise new NotFound())
    _ as n >> {
      doubled <- Effect.succeed(n * 2);
      return `${doubled}`;
    }
  };
  return verdict;
};
