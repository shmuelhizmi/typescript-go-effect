// provide composition in the run tail: merge two layers, then provide them,
// chained off a runtime call (SPEC §7.2 inline provide composition)

service Clock {
  now(): number
}

service Logger {
  info(msg: string): void
}

declare const runPromise: (e: unknown) => Promise<unknown>;

const ClockLive = layer Clock ({ now: () => 0 })
const LoggerLive = layer Logger ({ info: () => undefined })

effect main(): number requires Clock | Logger {
  clock <- Clock
  logger <- Logger
  logger.info("tick")
  return clock.now()
}

main() |> Effect.provide(Layer.merge(ClockLive, LoggerLive)) |> runPromise
