// atomic { } / atomic name() {} — STM transactions (SPEC §9, TRANSPILATION §13)
// raise inside atomic lowers to STM.fail/STM.die; binds stay yield*.

declare const balance: any; // TRef<number>
declare const other: any;   // TRef<number>

// block expression
const moveOne = atomic {
  current <- TRef.get(balance)
  if (current <= 0) raise new Error("insufficient")
  <- TRef.set(balance, current - 1)
  <- TRef.update(other, (n: number) => n + 1)
  return current
}

// declaration form
atomic transfer(amount: number): number {
  from <- TRef.get(balance)
  if (from < amount) raise.die new Error("overdraft")
  <- TRef.set(balance, from - amount)
  to <- TRef.get(other)
  <- TRef.set(other, to + amount)
  return amount
}

// nested: effect body inside switches the raise channel back to Effect
const program = effect {
  snapshot <- TRef.get(balance)
  committed <- STM.commit(atomic {
    v <- TRef.get(balance)
    if (v < 0) raise new Error("negative")
    return v
  })
  if (committed < 0) raise new Error("effect-channel")
  return snapshot + committed
}
