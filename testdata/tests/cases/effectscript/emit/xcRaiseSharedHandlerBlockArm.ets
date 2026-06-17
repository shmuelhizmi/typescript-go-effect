// shared-handler arm (T1 | T2) with a multi-statement BLOCK body that binds,
// followed by a `_` default that re-raises (SPEC §6 catchTags shared handler)

tagged error DbError {
  query: string
}

tagged error NetError {
  host: string
}

tagged error Fatal {
  detail: string
}

declare const runQuery: (sql: string) => any;
declare const retryHint: (e: unknown) => any;

effect query(sql: string): string {
  rows <- runQuery(sql) catch {
    // one shared handler for two tags, with a block body that binds first
    DbError | NetError as e >> {
      hint <- retryHint(e)
      return `degraded:${hint}`
    }
    // catch-all default that escalates to a fatal defect-free typed failure
    _ as e >> {
      <- Effect.logError(e)
      raise new Fatal({ detail: "unhandled" })
    }
  }
  return rows
}
