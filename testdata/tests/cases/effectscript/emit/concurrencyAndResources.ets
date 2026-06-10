// fork/join/par/race + using/defer (SPEC §8–9, TRANSPILATION §7–8)

declare const ea: any, eb: any, ec: any;
declare const Watchdog: any;

effect concurrency() {
  [a, b] <- par [ea, eb]
  named <- par { left: ea, right: eb }
  bounded <- par(4) [ea, eb, ec]
  winner <- race [ea, eb]
  first3 <- race [ea, eb, ec]

  fiber <- fork ea
  defer { <- Watchdog.interrupt(fiber) }
  joined <- join fiber

  return [a, b, named, bounded, winner, first3, joined]
}

effect resources() {
  using conn <- ea release (c, exit) { <- c.close() }
  defer (exit) { <- ec }
  return conn
}
