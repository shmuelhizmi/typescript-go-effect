// catch/match arm diagnostics (implemented subset; see SPEC §13 for the
// full planned set — 18110/18152/18153 arrive with native checking)

declare const ea: any;
declare const v: unknown;

effect arms() {
  a <- ea catch {
    _ as e   >> 0
    NotFound >> 1     // error TS18151: unreachable (follows catch-all arm)
  }

  const m = match (v) {
    _ >> 1
    2 >> 2            // error TS18151
  }

  const n = match tag (v) {
    notATag >> 1      // error TS18150: tag mode requires tag references or '_'
  }

  return [a, m, n]
}
