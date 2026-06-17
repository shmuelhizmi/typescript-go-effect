// raise.die (defect) vs raise (typed failure) side by side, in both statement
// and expression position (SPEC §5.2 raise vs raise.die)

tagged error Invalid {
  field: string
}

declare const parsePort: (raw: string) => number;

effect validate(raw: string): number raises Invalid {
  // statement position: typed failure
  if (raw === "") raise new Invalid({ field: "port" })
  // statement position: unrecoverable defect
  if (raw === "panic") raise.die new Error("unreachable config")
  port <- parsePort(raw)
  // expression position: defect on one branch, typed failure on the other
  const checked = port > 0 ? port : (port < -1 ? raise.die new Error("negative") : raise new Invalid({ field: "range" }))
  return checked
}
