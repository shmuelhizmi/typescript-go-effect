// An Effect.fn whose body has its own binds and embeds a nested Effect.gen
// that is itself yielded — both the outer fn and the inner gen must convert.
import { Effect } from "effect";

declare const ea: Effect.Effect<number>;
declare const eb: Effect.Effect<string>;

export const aggregate = Effect.fn("aggregate")(function* (seed: number) {
  const first = yield* ea;
  const inner = yield* Effect.gen(function* () {
    const label = yield* eb;
    return label.length + seed;
  });
  return first + inner;
});
