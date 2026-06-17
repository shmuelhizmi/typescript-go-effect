// Control flow (if / for / while) wrapping yields inside a gen body.
import { Effect } from "effect";

declare const ea: Effect.Effect<number>;
declare const eb: Effect.Effect<boolean>;
declare function pick(n: number): boolean;

export const program = Effect.gen(function* () {
  let acc = 0;

  const gate = yield* eb;
  if (gate) {
    const x = yield* ea;
    acc += x;
  } else {
    yield* ea;
  }

  for (let i = 0; i < 3; i++) {
    const step = yield* ea;
    if (pick(step)) {
      acc += step;
    }
  }

  while (acc < 100) {
    acc += yield* ea;
  }

  return acc;
});
