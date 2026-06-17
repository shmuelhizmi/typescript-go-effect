// An effect fn's `: A raises E requires R` return clause lowers to the
// generator's own return type `Effect.fn.Return<A, E, R>` (the type Effect.fn
// expects on its body). Omitted clauses default to never via the arg-count
// mapping: `: A` -> <A>, `: A raises E` -> <A, E>, `: A requires R` ->
// <A, never, R>. Also covers `raise (<- e)` — a raised parenthesized bind.

import { Effect } from "effect"

declare const lookup: (id: string) => Effect.Effect<string, NotFound, Db>
declare const Db: Effect.Effect<Db>
declare class NotFound { _tag: "NotFound" }
declare class Db {}

effect full(id: string): string raises NotFound requires Db {
  db <- Db
  return yield* lookup(id)
}

effect onlyRaises(id: string): string raises NotFound {
  return yield* lookup(id)
}

effect onlyRequires(id: string): number requires Db {
  db <- Db
  return 1
}

effect plain(id: string): string {
  return id
}

effect rethrow(id: string): string raises NotFound {
  raise (<- lookup(id))
}
