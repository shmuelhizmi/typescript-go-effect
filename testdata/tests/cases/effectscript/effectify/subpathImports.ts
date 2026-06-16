// Tree-shakeable subpath namespace imports (import * as Effect from
// "effect/Effect") — the form the forward desugarer now emits — are recognized
// exactly like the barrel form and convert the same way.
import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";
import * as Context from "effect/Context";

declare const ea: Effect.Effect<number>;

export const program = Effect.gen(function* () {
  const x = yield* ea;
  return x + 1;
});

export const load = Effect.fn("load")(function* (id: string) {
  const x = yield* ea;
  return x + id.length;
});

class Database extends Context.Tag("Database")<Database, { readonly url: string }>() {}

export const DatabaseLive = Layer.succeed(Database, { url: "localhost" });
