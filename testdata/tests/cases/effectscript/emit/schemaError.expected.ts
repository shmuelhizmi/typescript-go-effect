// `schema error Name { fields }` declares a Schema.TaggedError (a tagged error
// that is also a Schema), mirroring `schema` (Schema.Class) and `tagged error`
// (Data.TaggedError). `schema error {` keeps `error` as the schema name only
// when no declaration name follows.
import { Schema } from "effect";
export class BadRequest extends Schema.TaggedError<BadRequest>()("BadRequest", {
    message: Schema.String
}) {
}
class NotFound extends Schema.TaggedError<NotFound>()("NotFound", {
    id: Schema.String,
    status: Schema.Number
}) {
}
// plain schema is unaffected
class Person extends Schema.Class<Person>("Person")({
    name: Schema.String
}) {
}
// `error` as an ordinary schema name (no name follows the word `error`)
class error extends Schema.Class<error>("error")({
    v: Schema.Number
}) {
}
