// Effect.race (two-arg) reverses to `race [a, b]` and Effect.raceAll of three+
// reverses to `race [...]`. A two-element raceAll would desugar back to
// Effect.race(a, b), not raceAll, so it cannot round-trip and is left verbatim
// beside the converting siblings.
import { Effect } from "effect";

declare const ea: Effect.Effect<number>;
declare const eb: Effect.Effect<number>;
declare const ec: Effect.Effect<number>;

export const program = effect {
  duel <- race [ea, eb];
  fastest <- race [ea, eb, ec];
  // Two-element raceAll: round-trips to Effect.race, so left verbatim.
  pair <- Effect.raceAll([ea, eb]);
  return duel + fastest + pair;
};
