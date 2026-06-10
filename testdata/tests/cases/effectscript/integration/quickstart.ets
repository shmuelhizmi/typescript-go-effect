tagged error NotFound {
  id: string
}

tagged error RateLimited {}

service Greeter {
  greeting: Effect.Effect<string>
}

layer GreeterLive: Greeter {
  return { greeting: Effect.succeed("Hello") }
}

effect findUser(id: string): string raises NotFound {
  if (id === "") raise new NotFound({ id })
  return `user-${id}`
}

effect fetchGreeting(name: string): string requires Greeter {
  greeter <- Greeter
  prefix  <- greeter.greeting
  return `${prefix}, ${name}!`
}

effect main(): string requires Greeter {
  user <- findUser("42") catch {
    NotFound as e >> `fallback-${e.id}`
    _             >> "anonymous"
  }

  missing <- findUser("") catch {
    NotFound as e >> "missing!"
  }

  [g1, g2] <- par [fetchGreeting(user), fetchGreeting(missing)]

  fiber <- fork fetchGreeting("forked")
  forked <- join fiber

  defer { <- Effect.log("finalized") }

  const kind = match (user.length) {
    7  >> "seven"
    _  >> "other"
  }

  const tagged = match tag (new NotFound({ id: "x" }) as NotFound | RateLimited) {
    NotFound as e  >> `nf:${e.id}`
    RateLimited    >> "rl"
  }

  const doubled = 21 |> ((n: number) => n * 2)

  return [g1, g2, forked, kind, tagged, String(doubled)].join(" | ")
}

main()
  |> Effect.scoped
  |> Effect.provide(GreeterLive)
  |> Effect.runPromise
  |> ((p: Promise<string>) => p.then(console.log))
