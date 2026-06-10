// tagged error / schema declarations (SPEC §5.1, §7.4; TRANSPILATION §1.4, §1.5)

tagged error NotFound {
  id: string
}

tagged error RateLimited {}

export tagged error Forbidden {
  reason: string
  code: number
}

schema Person {
  name: Schema.String
  age:  Schema.Number
}

export schema Account {
  owner: Person
  tags:  Schema.Array(Schema.String),
}

// contextual keywords stay plain identifiers outside the declaration shapes
const tagged = 1;
const schema = (x: number) => x;
const error = schema(tagged);
const member = { tagged, error };

effect lookup(id: string): Person raises NotFound {
  if (id === "") raise new NotFound({ id })
  decoded <- Schema.decodeUnknown(Person)({ name: id, age: 1 })
  return decoded
}
