// Object-pattern match arms whose value is a parenthesized object literal
// `({ … })` followed by another `{`-led arm — the arrow-vs-object parser
// disambiguation must not let the next arm's `{` be eaten as a block body.
// Also: a pure match inside a plain arrow nested in an effect body stays pure
// (the concise arrow body is not an effect body).

import { Effect, Match } from "effect"

declare const input: { kind: "a" | "b"; n: number }

const classify = (i: typeof input) =>
  match (i) {
    { kind: "a" } as s >> ({ tag: "first", n: s.n })
    { kind: "b" } as s >> ({ tag: "second", n: s.n })
  }

const pick = effect {
  queue <- Effect.succeed({ mode: "prod" as "prod" | "dev", a: "x", b: "y" })
  const resolve = (q: typeof queue) =>
    match (q) {
      { mode: "prod" } as access >> access.a
      { mode: "dev" } as access >> access.b
    }
  return resolve(queue)
}
