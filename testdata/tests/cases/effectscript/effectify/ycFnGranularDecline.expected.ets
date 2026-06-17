// Granular decline: a convertible string-name Effect.fn next to a
// named-generator Effect.fn (the name is the span). The named-generator form
// has no faithful `effect` declaration, so it stays classic while the sibling
// still converts — proving per-declaration granularity.
import { Effect } from "effect";

declare const ea: Effect.Effect<number>;

// Convertible: string-name two-call form.
export effect measure(n: number) {
  base <- ea;
  return base + n;
}

// Not convertible: named generator carries the span name; left verbatim.
export const spanned = Effect.fn(function* spanned(n: number) {
  const base = yield* ea;
  return base * n;
});
