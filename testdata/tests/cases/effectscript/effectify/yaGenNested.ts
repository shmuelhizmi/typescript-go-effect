// A gen whose body yields a nested Effect.gen, both bound and discarded.
import { Effect } from "effect";

declare const ea: Effect.Effect<number>;
declare const eb: Effect.Effect<string>;

export const program = Effect.gen(function* () {
  const inner = yield* Effect.gen(function* () {
    const a = yield* ea;
    const b = yield* eb;
    return a + b.length;
  });

  yield* Effect.gen(function* () {
    yield* ea;
  });

  const total = inner + (yield* Effect.gen(function* () {
    return yield* ea;
  }));

  return total;
});
