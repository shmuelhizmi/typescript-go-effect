// Effect.all concurrency forms reversing to `par`: three-element array with a
// destructured result, a named-object collection, and a bounded par(n). A
// `{ discard: true }` Effect.all is an unrecognized option shape, so it stays
// verbatim alongside the converting siblings.
import { Effect } from "effect";

declare const ea: Effect.Effect<number>;
declare const eb: Effect.Effect<string>;
declare const ec: Effect.Effect<boolean>;

export const program = effect {
  [a, b, c] <- par [ea, eb, ec];
  named <- par { first: ea, second: eb, third: ec };
  bounded <- par(3) [ea, ea, ea, ea];
  // Unrecognized option (discard): not a `concurrency`-only object, left verbatim.
  dropped <- Effect.all([ea, eb], { discard: true });
  return a + b.length + (c ? 1 : 0) + named.first + bounded.length + (dropped ? 0 : 1);
};
