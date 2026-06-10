// Starter programs. The default is inlined so the editor renders instantly;
// the rest live in public/examples/ and are fetched on demand.
import { decodeSourceFromHash } from "./state/share"

export const DEFAULT_SOURCE = `import { Data } from "effect";

class NotFound extends Data.TaggedError("NotFound")<{ id: string }> {}

service Greeter {
  greeting: Effect.Effect<string>
}

layer GreeterLive: Greeter {
  return { greeting: Effect.succeed("Hello") }
}

effect findUser(id: string): string raises NotFound {
  if (id === "") raise new NotFound({ id })
  return \`user-\${id}\`
}

effect greet(name: string): string requires Greeter {
  greeter <- Greeter
  prefix  <- greeter.greeting
  return \`\${prefix}, \${name}!\`
}

effect main(): string requires Greeter {
  user <- findUser("42") catch {
    NotFound as e >> \`fallback-\${e.id}\`
  }

  [a, b] <- par [greet(user), greet("world")]

  defer { <- Effect.log("done") }

  return \`\${a} | \${b}\`
}

main()
  |> Effect.scoped
  |> Effect.provide(GreeterLive)
  |> Effect.runPromise
  |> ((p: Promise<string>) => p.then(console.log))
`

export interface Example {
  id: string
  label: string
}

export const EXAMPLES: Example[] = [
  { id: "greeter", label: "Greeter (default)" },
  { id: "quickstart", label: "Quickstart (kitchen sink)" },
]

export async function loadExample(id: string): Promise<string> {
  if (id === "greeter") return DEFAULT_SOURCE
  const response = await fetch(`/examples/${id}.ets`)
  if (!response.ok) throw new Error(`failed to load example ${id}: ${response.status}`)
  return response.text()
}

/** Source restored from a share link, falling back to the default program. */
export const initialSource: string = decodeSourceFromHash() ?? DEFAULT_SOURCE
