// bind followed by an early `return` inside a branch, binds nested in a
// try/catch, and a discard bind guarding a conditional return.

declare const probe: any;
declare function risky(n: number): any;
declare function cleanup(): any;

effect earlyReturn(n: number): number {
  status <- probe

  // early return out of a guarded branch, after a bind in that branch
  if (status < 0) {
    fallback <- risky(0)
    return fallback
  }

  // bind inside a try/catch; control flow + try survive lowering
  let acc = status
  try {
    value <- risky(n)
    acc = acc + value
  } catch (e) {
    recovered <- risky(-1)
    acc = acc + recovered
  } finally {
    <- cleanup()
  }

  return acc
}
