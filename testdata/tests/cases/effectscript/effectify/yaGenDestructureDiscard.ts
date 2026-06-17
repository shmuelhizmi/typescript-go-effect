// Destructuring yields (object + array, nested + renamed + defaults) and a
// discard yield sequenced between them.
import { Effect } from "effect";

declare const obj: Effect.Effect<{ a: number; b: { c: string } }>;
declare const tup: Effect.Effect<[number, string, boolean]>;
declare const ren: Effect.Effect<{ value: number }>;
declare const ea: Effect.Effect<number>;

export const program = Effect.gen(function* () {
  const { a, b: { c } } = yield* obj;
  yield* ea;
  const [first, , third] = yield* tup;
  const { value: renamed = 0 } = yield* ren;
  return a + c.length + first + (third ? 1 : 0) + renamed;
});
