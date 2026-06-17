// A subpath namespace import bound to a NON-canonical local name
// (`import * as Lyr from "effect/Layer"`) is recognized as the Layer helper, but
// `effect`/`layer` desugaring is canonical-only — it can only ever re-emit
// `Layer`, never `Lyr`. So a *value* use of `Lyr.succeed` would force a global
// round-trip bail. Here `Lyr` appears only in a TYPE position, which the
// desugarer never rewrites, so it stays byte-verbatim while the canonical
// subpath `Effect` sibling converts.
import * as Effect from "effect/Effect";
import * as Lyr from "effect/Layer";

declare const ea: Effect.Effect<number>;
declare const mkLive: () => Lyr.Layer<number>;

export const program = Effect.gen(function* () {
  const x = yield* ea;
  return x + 1;
});
