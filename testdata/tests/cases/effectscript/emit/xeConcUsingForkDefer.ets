// a using-resource inside an effect that also forks, plus both defer forms (SPEC §8–9)

declare const openPool: any, background: any, cleanup: any;

effect xeConcUsingForkDefer() {
  using pool <- openPool() release (p) { <- p.drain() }
  worker <- fork background(pool)
  defer { <- cleanup(pool) }
  defer (exit) { <- cleanup(exit) }
  done <- join worker
  return [pool, done]
}
