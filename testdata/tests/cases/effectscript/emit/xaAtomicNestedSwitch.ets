// Nested body kinds flip the raise channel: an effect{} nested inside an
// atomic{} switches raise back to Effect.fail, and an atomic{} nested inside
// an effect{} switches it to STM.fail — the innermost enclosing body wins.
import * as TRef from "effect/TRef";

declare const gate: any; // TRef<number>

// atomic outer, effect inner: outer raise -> STM.fail, inner raise -> Effect.fail
const outerAtomic = atomic {
  level <- TRef.get(gate)
  if (level < 0) raise new Error("stm-channel")
  inner <- STM.commit(effect {
    if (level > 100) raise new Error("effect-channel")
    return level * 2
  })
  return inner
}

// effect outer, atomic inner: outer raise -> Effect.fail, inner raise -> STM.fail
const outerEffect = effect {
  seed <- TRef.get(gate)
  if (seed < 0) raise new Error("effect-channel")
  doubled <- STM.commit(atomic {
    if (seed > 100) raise new Error("stm-channel")
    return seed + seed
  })
  return doubled
}
