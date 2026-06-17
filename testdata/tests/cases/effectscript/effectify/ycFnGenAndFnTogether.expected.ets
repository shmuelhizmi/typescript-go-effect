// A bare Effect.gen program and a string-name Effect.fn living in the same
// file — both must convert: the gen to `effect { ... }`, the fn to
// `effect name(...) { ... }`.
import { Effect } from "effect";

declare const ea: Effect.Effect<number>;
declare const eb: Effect.Effect<string>;

// Plain generator program → effect block expression.
export const boot = effect {
  n <- ea;
  s <- eb;
  return n + s.length;
};

// String-name fn → effect declaration.
export effect greet(name: string) {
  n <- ea;
  return name.length + n;
}
