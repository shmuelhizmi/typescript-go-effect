// service + layer declarations (SPEC §7, TRANSPILATION §5–6)

declare const cfg: { dbUrl: string };
declare const createPool: (cfg: unknown) => any;

service Database {
  query(sql: string): unknown[] raises DbError
  readonly url: string
}

export service Cache {
  get(key: string): string | undefined
}

declare class DbError { readonly _tag: "DbError" }
declare const PgPool: any;

layer DatabaseLive: Database {
  pool <- PgPool
  return {
    query: (sql: string) => pool.query(sql),
    url: cfg.dbUrl,
  }
}

scoped layer PgPoolLive: Database {
  using pool <- createPool(cfg) release (p) { <- p.end() }
  return pool
}

layer CacheTest: Cache = { get: () => undefined }

layer Wired: Database provide [CacheTest] {
  return (<- PgPool)
}
