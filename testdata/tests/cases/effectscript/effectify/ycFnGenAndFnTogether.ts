// A bare Effect.gen program and a string-name Effect.fn living in the same
// file — both must convert: the gen to `effect { ... }`, the fn to
// `effect name(...) { ... }`.
import { Effect } from "effect";

declare const ea: Effect.Effect<number>;
declare const eb: Effect.Effect<string>;

// Plain generator program → effect block expression.
export const boot = Effect.gen(function* () {
  const n = yield* ea;
  const s = yield* eb;
  return n + s.length;
});

// String-name fn → effect declaration.
export const greet = Effect.fn("greet")(function* (name: string) {
  const n = yield* ea;
  return name.length + n;
});
