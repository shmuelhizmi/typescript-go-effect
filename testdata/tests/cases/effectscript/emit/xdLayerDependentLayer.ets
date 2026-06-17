// a layer whose body accesses another service before returning its impl
// (inter-layer dependency: Logger <- Logger inside the layer body)

service Logger {
  info(msg: string): void
}

service Greeter {
  greet(name: string): string
}

layer GreeterLive: Greeter {
  log <- Logger
  return {
    greet: (name: string) => {
      log.info("greeting " + name)
      return "hi " + name
    },
  }
}
