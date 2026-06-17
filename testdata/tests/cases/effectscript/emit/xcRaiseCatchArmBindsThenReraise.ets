// catch arms whose handler binds (<-) before yielding, plus re-raising a
// DIFFERENT tagged error from inside an arm (SPEC §6 catch handler bodies)

tagged error NotFound {
  id: string
}

tagged error Backend {
  cause: string
}

declare const fetchRow: (id: string) => any;
declare const auditMiss: (id: string) => any;
declare const reportRow: (row: unknown) => string;

effect resolve(id: string): string {
  out <- fetchRow(id) catch {
    // arm body binds via <- before returning a recovered value
    NotFound as e >> {
      logged <- auditMiss(e.id)
      return `recovered:${logged}`
    }
    // arm body binds, then re-raises a DIFFERENT tagged error
    Backend as e >> {
      detail <- reportRow(e.cause)
      raise new NotFound({ id: detail })
    }
  }
  return out
}
