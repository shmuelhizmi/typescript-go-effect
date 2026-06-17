// Multiple distinct subpath namespace imports (Effect + Layer + Context) all
// recognized in one file, exercised together with catchTag, Effect.all and
// Layer.provide — the deeper combination subpathImports.ts does not cover.
import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";
import * as Context from "effect/Context";

class NotFound {
  readonly _tag = "NotFound";
}

declare const ea: Effect.Effect<number>;
declare const eb: Effect.Effect<string, NotFound>;

service Config { readonly url: string }
service Database { readonly query: () => Effect.Effect<number> }

export const program = effect {
  cfg <- Config;
  res <- eb catch {
    NotFound >> "missing"
  };
  [a, b] <- par [ea, ea];
  return cfg.url.length + res.length + a + b;
};

export layer ConfigLive: Config = { url: "localhost" };

export const DatabaseLive = Layer.effect(Database, effect {
  cfg <- Config;
  return { query: () => ea } as { readonly query: () => Effect.Effect<number> };
}) |> Layer.provide(ConfigLive);
