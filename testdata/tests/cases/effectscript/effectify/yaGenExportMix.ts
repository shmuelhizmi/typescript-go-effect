// One file with an exported const gen and a top-level (unexported) gen, where
// the unexported one is yielded into the exported one.
import { Effect } from "effect";

declare const ea: Effect.Effect<number>;
declare const ec: Effect.Effect<number[]>;

const helper = Effect.gen(function* () {
  const xs = yield* ec;
  return xs.length;
});

export const program = Effect.gen(function* () {
  const n = yield* helper;
  const base = yield* ea;
  return n + base;
});
