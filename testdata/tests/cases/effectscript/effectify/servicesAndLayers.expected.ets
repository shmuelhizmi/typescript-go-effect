// services, layers (effect/scoped/succeed/provide) and resources
import { Context, Effect, Layer } from "effect";

interface Rows {
  rows: string[];
}
class DbError {
  readonly _tag = "DbError";
}

export service Database {
  query(sql: string): Rows raises DbError;
  readonly url: string;
}

service Config {
  readonly dbUrl: string;
}

declare const mkPool: (url: string) => Effect.Effect<{ query: (sql: string) => Effect.Effect<Rows, DbError>; close: () => Effect.Effect<void> }>;

export layer ConfigLive: Config = { dbUrl: "postgres://localhost" };

export layer DatabaseLive: Database {
  cfg <- Config;
  pool <- mkPool(cfg.dbUrl);
  return {
    query: (sql: string) => pool.query(sql),
    url: cfg.dbUrl,
  };
}

scoped layer ScopedDb: Database {
  cfg <- Config;
  using pool <- mkPool(cfg.dbUrl) release (p) {
    <- p.close();
  }
  defer {
    <- Effect.log("closing db");
  }
  defer (exit) {
    <- Effect.log(`exit: ${exit}`);
  }
  return {
    query: (sql: string) => pool.query(sql),
    url: cfg.dbUrl,
  };
}

layer MainLive: Database provide [ConfigLive, ScopedDb] {
  db <- Database;
  return db;
}
