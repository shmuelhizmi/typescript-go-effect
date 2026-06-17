// Mixed barrel imports with a renamed specifier: `Layer as L`. EffectScript's
// `layer` declaration lowers only to the canonical `Layer.succeed`, never
// `L.succeed`, so a VALUE use of `L` would force a whole-file round-trip bail
// (one un-reproducible convertible usage skips the entire file). Here the `L`
// binding is used only in TYPE positions — which the forward desugarer never
// touches — so the renamed import and its type uses stay byte-verbatim while
// the canonical `Effect` binding converts.
import { Effect, Layer as L } from "effect";

declare const ea: Effect.Effect<number>;
declare const baseLayer: L.Layer<number>;
declare function provideLayer(l: L.Layer<number>): L.Layer<number>;

export const program = effect {
  x <- ea;
  return x + 1;
};
