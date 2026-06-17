// Effect.race (two-arg) reverses to `race [a, b]` and Effect.raceAll of three+
// reverses to `race [...]`. A two-element raceAll would desugar back to
// Effect.race(a, b), not raceAll, so it cannot round-trip and is left verbatim
// beside the converting siblings.
import { Effect } from "effect";

declare const ea: Effect.Effect<number>;
declare const eb: Effect.Effect<number>;
declare const ec: Effect.Effect<number>;

export const program = Effect.gen(function* () {
  const duel = yield* Effect.race(ea, eb);
  const fastest = yield* Effect.raceAll([ea, eb, ec]);
  // Two-element raceAll: round-trips to Effect.race, so left verbatim.
  const pair = yield* Effect.raceAll([ea, eb]);
  return duel + fastest + pair;
});
