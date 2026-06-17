// A multi-stage combinator pipe (map/flatMap/tap/withSpan) reverses to a `|>`
// chain (ConvertPipes is on), and a Layer.effect service definition reverses
// to a `layer` declaration with its gen body.
import { Context, Effect, Layer } from "effect";

class DbError {
  readonly _tag = "DbError";
}

declare const program: Effect.Effect<number, DbError, never>;
declare function double(n: number): number raises DbError;

// Multi-stage combinator tail: every stage is a tracked Effect helper, so the
// whole chain becomes `program |> Effect.map(...) |> Effect.flatMap(...) |> ...`.
export const pipeline = program |> Effect.map((n) => n + 1) |> Effect.flatMap((n) => double(n)) |> Effect.tap((n) => Effect.logInfo(`got ${n}`)) |> Effect.withSpan("pipeline");

interface Counter {
  readonly next: () => Effect.Effect<number>;
}

export service Seed { readonly start: number }
export class Counters extends Context.Tag("Counters")<Counters, Counter>() {}

declare const mkCounter: (start: number) => Effect.Effect<Counter, DbError>;

// Layer.effect with a gen body reverses to a `layer ... : Service { ... }`.
export layer CountersLive: Counters {
  seed <- Seed;
  counter <- mkCounter(seed.start);
  return counter;
}
