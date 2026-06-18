// `schema error Name { fields }` declares a Schema.TaggedError (a tagged error
// that is also a Schema), mirroring `schema` (Schema.Class) and `tagged error`
// (Data.TaggedError). `schema error {` keeps `error` as the schema name only
// when no declaration name follows.

import { Schema } from "effect"

export schema error BadRequest {
  message: Schema.String
}

schema error NotFound {
  id: Schema.String
  status: Schema.Number
}

// plain schema is unaffected
schema Person {
  name: Schema.String
}

// `error` as an ordinary schema name (no name follows the word `error`)
schema error {
  v: Schema.Number
}
