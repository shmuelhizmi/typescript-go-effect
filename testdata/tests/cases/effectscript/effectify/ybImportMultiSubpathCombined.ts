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

class Config extends Context.Tag("Config")<Config, { readonly url: string }>() {}
class Database extends Context.Tag("Database")<Database, { readonly query: () => Effect.Effect<number> }>() {}

export const program = Effect.gen(function* () {
  const cfg = yield* Config;
  const res = yield* eb.pipe(
    Effect.catchTag("NotFound", (_) => Effect.gen(function* () {
      return "missing";
    })),
  );
  const [a, b] = yield* Effect.all([ea, ea], { concurrency: "unbounded" });
  return cfg.url.length + res.length + a + b;
});

export const ConfigLive = Layer.succeed(Config, { url: "localhost" });

export const DatabaseLive = Layer.effect(Database, Effect.gen(function* () {
  const cfg = yield* Config;
  return { query: () => ea } as { readonly query: () => Effect.Effect<number> };
})).pipe(Layer.provide(ConfigLive));
