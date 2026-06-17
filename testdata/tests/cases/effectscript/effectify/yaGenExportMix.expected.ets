// One file with an exported const gen and a top-level (unexported) gen, where
// the unexported one is yielded into the exported one.
import { Effect } from "effect";

declare const ea: Effect.Effect<number>;
declare const ec: Effect.Effect<number[]>;

const helper = effect {
  xs <- ec;
  return xs.length;
};

export const program = effect {
  n <- helper;
  base <- ea;
  return n + base;
};
