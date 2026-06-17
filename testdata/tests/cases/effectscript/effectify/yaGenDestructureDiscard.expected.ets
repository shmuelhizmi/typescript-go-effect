// Destructuring yields (object + array, nested + renamed + defaults) and a
// discard yield sequenced between them.
import { Effect } from "effect";

declare const obj: Effect.Effect<{ a: number; b: { c: string } }>;
declare const tup: Effect.Effect<[number, string, boolean]>;
declare const ren: Effect.Effect<{ value: number }>;
declare const ea: Effect.Effect<number>;

export const program = effect {
  { a, b: { c } } <- obj;
  <- ea;
  [first, , third] <- tup;
  { value: renamed = 0 } <- ren;
  return a + c.length + first + (third ? 1 : 0) + renamed;
};
