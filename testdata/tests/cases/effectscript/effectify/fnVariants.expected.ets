// Effect.fn call shapes. EffectScript's `effect` declaration desugars to the
// string-name two-call form `Effect.fn("n")(function* () {})`, so only shapes
// that desugar back to themselves are convertible — the structural round-trip
// verifier leaves the rest verbatim rather than silently changing them.
import { Effect } from "effect";

// String-name form → effect declaration.
export effect load(id: string) {
  return id.length;
}

// Anonymous generator → anonymous effect expression.
export const wrap = effect (n: number) {
  return n + 1;
};

// Named generator (the name is the span): no string-name EffectScript form
// exists, so it stays a classic Effect.fn — converting it would desugar to the
// string form and fail the round trip, taking the whole file down with it.
export const named = Effect.fn(function* named(n: number) {
  return n - 1;
});
