// A gen whose body yields a nested Effect.gen, both bound and discarded.
import { Effect } from "effect";

declare const ea: Effect.Effect<number>;
declare const eb: Effect.Effect<string>;

export const program = effect {
  inner <- effect {
    a <- ea;
    b <- eb;
    return a + b.length;
  };

  <- effect {
    <- ea;
  };

  const total = inner + (<- effect {
    return (<- ea);
  });

  return total;
};
