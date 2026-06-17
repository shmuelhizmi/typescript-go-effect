// Effect.all concurrency forms reversing to `par`: three-element array with a
// destructured result, a named-object collection, and a bounded par(n). A
// `{ discard: true }` Effect.all is an unrecognized option shape, so it stays
// verbatim alongside the converting siblings.
import { Effect } from "effect";

declare const ea: Effect.Effect<number>;
declare const eb: Effect.Effect<string>;
declare const ec: Effect.Effect<boolean>;

export const program = Effect.gen(function* () {
  const [a, b, c] = yield* Effect.all([ea, eb, ec], { concurrency: "unbounded" });
  const named = yield* Effect.all({ first: ea, second: eb, third: ec }, { concurrency: "unbounded" });
  const bounded = yield* Effect.all([ea, ea, ea, ea], { concurrency: 3 });
  // Unrecognized option (discard): not a `concurrency`-only object, left verbatim.
  const dropped = yield* Effect.all([ea, eb], { discard: true });
  return a + b.length + (c ? 1 : 0) + named.first + bounded.length + (dropped ? 0 : 1);
});
