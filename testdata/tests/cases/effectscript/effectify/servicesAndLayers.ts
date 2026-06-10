// services, layers (effect/scoped/succeed/provide) and resources
import { Context, Effect, Layer } from "effect";

interface Rows {
  rows: string[];
}
class DbError {
  readonly _tag = "DbError";
}

export class Database extends Context.Tag("Database")<Database, {
  query(sql: string): Effect.Effect<Rows, DbError>;
  readonly url: string;
}>() {}

class Config extends Context.Tag("Config")<Config, {
  readonly dbUrl: string;
}>() {}

declare const mkPool: (url: string) => Effect.Effect<{ query: (sql: string) => Effect.Effect<Rows, DbError>; close: () => Effect.Effect<void> }>;

export const ConfigLive = Layer.succeed(Config, { dbUrl: "postgres://localhost" });

export const DatabaseLive = Layer.effect(Database, Effect.gen(function* () {
  const cfg = yield* Config;
  const pool = yield* mkPool(cfg.dbUrl);
  return {
    query: (sql: string) => pool.query(sql),
    url: cfg.dbUrl,
  };
}));

const ScopedDb = Layer.scoped(Database, Effect.gen(function* () {
  const cfg = yield* Config;
  const pool = yield* Effect.acquireRelease(mkPool(cfg.dbUrl), (p) => Effect.gen(function* () {
    yield* p.close();
  }));
  yield* Effect.addFinalizer(() => Effect.gen(function* () {
    yield* Effect.log("closing db");
  }));
  yield* Effect.addFinalizer((exit) => Effect.gen(function* () {
    yield* Effect.log(`exit: ${exit}`);
  }));
  return {
    query: (sql: string) => pool.query(sql),
    url: cfg.dbUrl,
  };
}));

const MainLive = Layer.effect(Database, Effect.gen(function* () {
  const db = yield* Database;
  return db;
})).pipe(Layer.provide([ConfigLive, ScopedDb]));
