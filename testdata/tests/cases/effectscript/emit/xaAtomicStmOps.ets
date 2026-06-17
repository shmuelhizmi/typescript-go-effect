// STM-specific ops inside one atomic body: STM.retry as a bound statement,
// several TRef reads/writes, a conditional STM.fail (raise) and a conditional
// STM.die (raise.die). Binds stay yield*; both raise forms pick the STM channel.
import * as TRef from "effect/TRef";

declare const supply: any;  // TRef<number>
declare const demand: any;  // TRef<number>
declare const filled: any;  // TRef<number>

const match = atomic {
  available <- TRef.get(supply)
  wanted <- TRef.get(demand)
  if (available === 0) <- STM.retry
  if (wanted < 0) raise.die new Error("corrupt demand")
  if (wanted > available) raise new Error("oversold")
  <- TRef.set(supply, available - wanted)
  <- TRef.update(filled, (n: number) => n + wanted)
  remaining <- TRef.get(supply)
  return remaining
}

// commit the STM transaction from an effect{} so the lowering still imports
// effect/Effect (and the migrator can round-trip it).
const run = effect {
  left <- STM.commit(match)
  return left
}
