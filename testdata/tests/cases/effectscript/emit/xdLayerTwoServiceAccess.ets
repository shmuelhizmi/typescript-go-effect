// an effect that requires two services and accesses both inside the body
// (two distinct `x <- Service` binds + a method-call bind)

service Config {
  port(): number
}

service Logger {
  info(msg: string): void
}

effect boot(): string requires Config | Logger {
  config <- Config
  logger <- Logger
  p <- config.port()
  logger.info("listening on " + p)
  return "started:" + p
}
