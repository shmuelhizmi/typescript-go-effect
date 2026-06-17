// Control flow (if / for / while) wrapping yields inside a gen body.
import { Effect } from "effect";

declare const ea: Effect.Effect<number>;
declare const eb: Effect.Effect<boolean>;
declare function pick(n: number): boolean;

export const program = effect {
  let acc = 0;

  gate <- eb;
  if (gate) {
    x <- ea;
    acc += x;
  } else {
    <- ea;
  }

  for (let i = 0; i < 3; i++) {
    step <- ea;
    if (pick(step)) {
      acc += step;
    }
  }

  while (acc < 100) {
    acc += (<- ea);
  }

  return acc;
};
