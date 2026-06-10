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
type LookupResult = Effect.Effect<Fetched, NotFound, typeof Match>;

declare function fetchQuote(symbol: string): Effect.Effect<number, NotFound | DbError, never>;

export function lookup(id: string): Effect.Effect<Fetched, NotFound> {
  return Effect.succeed(id);
}

export const main = pipe(program, Effect.retry(policy), Effect.runPromise);

export const piped = program.pipe(Effect.retry(policy), Effect.withSpan("piped"));

declare const res: { status: number } | string | NotFound | DbError | NetError;

export const label = Match.value(res).pipe(
  Match.when("timeout", (_) => "timed out"),
  Match.whenOr(301, 302, (_) => "redirect"),
  Match.tag("NotFound", (e) => e.id),
  ((h) => Match.tags({ DbError: h, NetError: h }))((_) => "infra down"),
  Match.orElse((other) => `unknown: ${other}`),
);

declare const code: 200 | 404 | 500;

export const exhaustive = Match.value(code).pipe(
  Match.when(200, (_) => "ok"),
  Match.when(404, (_) => "missing"),
  Match.when(500, (_) => "boom"),
  Match.exhaustive,
);

export const effectful = Effect.gen(function* () {
  const quote = yield* fetchQuote("AAPL");
  const verdict = (yield* Match.value(quote).pipe(
    Match.when(0, (_) => Effect.gen(function* () {
      return yield* Effect.fail(new NotFound());
    })),
    Match.orElse((n) => Effect.gen(function* () {
      const doubled = yield* Effect.succeed(n * 2);
      return `${doubled}`;
    })),
  ));
  return verdict;
});
