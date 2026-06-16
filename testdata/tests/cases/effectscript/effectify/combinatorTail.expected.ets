// A combinator-tail Effect.fn has no faithful `effect`-declaration form
// (EffectScript dropped decorators), so it must be left verbatim while the rest
// of the file still converts.
import { Effect } from "effect";

declare const withSpan: <A, E, R>(name: string) => (self: Effect.Effect<A, E, R>) => Effect.Effect<A, E, R>;

// Convertible: plain named effect fn.
export effect load(id: string) {
  return id.length;
}

// Not convertible: trailing combinator argument. Left as classic Effect.fn.
export const traced = Effect.fn("traced")(function* (id: string) {
  return id.length;
}, withSpan("traced"));
