import * as TRef from "effect/TRef"

// Software-transactional memory: `atomic { }` runs an STM transaction, and
// `raise` inside it fails the transaction (STM.fail) so nothing commits.

// An atomic transfer over two transactional refs — all-or-nothing.
atomic transfer(from: TRef.TRef<number>, to: TRef.TRef<number>, amount: number): number {
  balance <- TRef.get(from)
  if (balance < amount) raise new Error(`insufficient: ${balance} < ${amount}`)
  <- TRef.set(from, balance - amount)
  current <- TRef.get(to)
  <- TRef.set(to, current + amount)
  return amount
}

effect main(): string {
  from <- TRef.make(100)
  to   <- TRef.make(0)

  // Run transfers concurrently; STM serializes them with no lost updates.
  <- par [
    STM.commit(transfer(from, to, 30)),
    STM.commit(transfer(from, to, 20)),
    STM.commit(transfer(from, to, 50))
  ]

  // Read both balances in a single atomic snapshot.
  [a, b] <- STM.commit(atomic {
    x <- TRef.get(from)
    y <- TRef.get(to)
    return [x, y] as const
  })

  return `from=${a} to=${b}`
}

main()
  |> Effect.runPromise
  |> ((p: Promise<string>) => p.then(console.log))
