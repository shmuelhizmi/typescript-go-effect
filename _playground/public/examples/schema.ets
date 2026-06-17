// `schema` declares an Effect Schema class: a runtime validator, a constructor,
// and a TypeScript type all at once. `Schema` is auto-imported.

schema Person {
  name: Schema.String
  age:  Schema.Number
}

// Decode untrusted input into a typed Person, recovering from validation errors.
effect describe(input: unknown): string {
  person <- Schema.decodeUnknown(Person)(input) catch {
    _ >> null
  }
  if (person === null) return "<invalid>"
  return `${person.name} (${person.age})`
}

effect main(): string {
  // Construct directly when the data is already trusted.
  const ada = new Person({ name: "Ada", age: 36 })

  good <- describe({ name: "Bea", age: 27 })
  bad  <- describe({ name: "Cy", age: "oops" })

  return [`${ada.name} (${ada.age})`, good, bad].join("\n")
}

main()
  |> Effect.runPromise
  |> ((p: Promise<string>) => p.then(console.log))
