// raise statements and expressions (Effect.fail / Effect.die)
import { Effect } from "effect";

class Boom {
  readonly _tag = "Boom";
}

export const failing = effect {
  n <- Effect.succeed(1);
  if (n > 0) {
    raise new Boom();
  }
  raise.die "boom";
};

export const expr = effect {
  const x = (<- Effect.succeed(1)) > 0 ? 1 : (raise new Boom());
  return x;
};
