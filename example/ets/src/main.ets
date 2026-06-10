// EffectScript sample — exercises the core constructs against the real
// `effect` npm package. See docs/effectscript/ for the full language docs,
// and testdata/tests/cases/effectscript/integration/quickstart.ets for the
// complete kitchen-sink program.
tagged error NotFound {
  id: string
}

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

effect greet(name: string): string requires Greeter {
  greeter <- Greeter
  prefix  <- greeter.greeting
  return `${prefix}, ${name}!`
}

effect main(): string requires Greeter {
  user <- findUser("42") catch {
    NotFound as e >> `fallback-${e.id}`
  }

  [a, b] <- par [greet(user), greet("world")]

  defer { <- Effect.log("done") }

  const kind = match (user.length) {
    7 >> "seven"
    _ >> "other"
  }

  return [a, b, kind].join(" | ")
}

main()
  |> Effect.scoped
  |> Effect.provide(GreeterLive)
  |> Effect.runPromise
  |> ((p: Promise<string>) => p.then(console.log))
