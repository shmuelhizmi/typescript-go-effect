import { Schema } from "effect";

// The exact desugared `schema error` shape reverses to the sugar: the `<Name>`
// self type argument and the tag string both == class name, an object-literal
// field argument, and an empty class body.
export class BadRequest extends Schema.TaggedError<BadRequest>()("BadRequest", {
	message: Schema.String,
}) {}

class NotFound extends Schema.TaggedError<NotFound>()("NotFound", { id: Schema.String, status: Schema.Number }) {}

// Non-matching: tag string differs from the class name → stays verbatim.
export class Mislabeled extends Schema.TaggedError<Mislabeled>()("Other", { x: Schema.Number }) {}

// Schema.Class (not TaggedError) still reverses to plain `schema`.
export class Person extends Schema.Class<Person>("Person")({ name: Schema.String }) {}
