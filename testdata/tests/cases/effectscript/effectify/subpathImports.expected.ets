// Tree-shakeable subpath namespace imports (import * as Effect from
// "effect/Effect") — the form the forward desugarer now emits — are recognized
// exactly like the barrel form and convert the same way.
import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";
import * as Context from "effect/Context";

declare const ea: Effect.Effect<number>;

export const program = effect {
  x <- ea;
  return x + 1;
};

export effect load(id: string) {
  x <- ea;
  return x + id.length;
}

service Database { readonly url: string }

export layer DatabaseLive: Database = { url: "localhost" };
