// a service whose method is itself an effect with parameters, consumed via `<-`
// (method return uses `raises` sugar -> Effect.Effect<...>; called result is yielded)

declare class NotFound { readonly _tag: "NotFound" }

service UserRepo {
  findById(id: string): string raises NotFound
}

effect loadName(id: string): string requires UserRepo {
  repo <- UserRepo
  name <- repo.findById(id)
  return name.toUpperCase()
}
