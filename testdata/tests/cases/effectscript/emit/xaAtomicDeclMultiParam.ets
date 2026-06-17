// atomic declaration form with multiple params + return type lowers to an
// arrow returning STM.gen; calling it yields an STM value that an enclosing
// effect{} commits with STM.commit.
import * as TRef from "effect/TRef";

declare const ledger: any;

// multi-param + explicit return type
atomic withdraw(amount: number, fee: number): number {
  current <- TRef.get(ledger)
  total <- STM.succeed(amount + fee)
  if (current < total) raise.die new Error("insufficient")
  <- TRef.set(ledger, current - total)
  return total
}

// call the atomic declaration and commit it from inside an effect{}
const settle = effect {
  charged <- STM.commit(withdraw(40, 2))
  if (charged <= 0) raise new Error("nothing charged")
  return charged
}
