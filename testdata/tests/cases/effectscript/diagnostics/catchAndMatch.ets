// catch/match arm diagnostics

import { Data } from "effect";

class NotFound extends Data.TaggedError("NotFound")<{}> {}
class Plain {} // no _tag

declare const ea: any;
declare const v: unknown;

effect bad() {
  a <- ea catch {
    Plain >> 1            // error TS18110: tag must be a tagged error class
    _ >> 2
    NotFound >> 3         // error TS18151: unreachable arm (follows catch-all)
  }

  const m = match (v) {
    badCase >> 1          // ok: lowercase binding... but then:
    NotFound >> 2         // error TS18151: unreachable (binding arm above catches all)
  }

  const n = match tag (v) {
    notATag >> 1          // error TS18150: tag mode requires capitalized tag references
  }

  return [a, m, n]
}

function pure() {
  // effectful arm outside effect body
  const x = match (v) {
    _ >> raise new NotFound({})   // error TS18153
  }
  return x
}

// non-exhaustive match (no '_' arm, union not covered) -> TS18152 via checker
declare const err: NotFound;
effect nonExhaustive() {
  return match tag (err) {
  }                       // error TS18152
}
