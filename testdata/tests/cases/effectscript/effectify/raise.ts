// raise statements and expressions (Effect.fail / Effect.die)
import { Effect } from "effect";

class Boom {
  readonly _tag = "Boom";
}

export const failing = Effect.gen(function* () {
  const n = yield* Effect.succeed(1);
  if (n > 0) {
    return yield* Effect.fail(new Boom());
  }
  return yield* Effect.die("boom");
});

export const expr = Effect.gen(function* () {
  const x = (yield* Effect.succeed(1)) > 0 ? 1 : (yield* Effect.fail(new Boom()));
  return x;
});
