// atomic{} used directly as a bound expression: STM.commit(atomic{ ... }) on
// the right of a `<-` bind inside an effect{}, including an inline atomic whose
// own body binds and raises on the STM channel, then a second committed inline
// atomic feeding the return.
import * as TRef from "effect/TRef";

declare const counter: any; // TRef<number>
declare const limit: any;   // TRef<number>

const bump = effect {
  // inline atomic as the bound expression — its raise stays STM.fail
  next <- STM.commit(atomic {
    cur <- TRef.get(counter)
    cap <- TRef.get(limit)
    if (cur >= cap) raise new Error("at capacity")
    <- TRef.set(counter, cur + 1)
    return cur + 1
  })
  if (next < 0) raise new Error("effect-channel underflow")
  // a second inline atomic committed and folded into the result
  snapshot <- STM.commit(atomic {
    v <- TRef.get(counter)
    return v
  })
  return next + snapshot
}
