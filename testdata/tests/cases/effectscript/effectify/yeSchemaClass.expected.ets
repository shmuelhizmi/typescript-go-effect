// Schema.Class declarations whose tag string and <Self> type argument both
// equal the class name, with an empty body, reverse to `schema Name { … }`.
// Tagged classes, named base shapes and bodies with members stay verbatim.
import { Schema } from "effect";

export schema RefreshTokenSuccess {
	type: Schema.Literal("success"),
	accessToken: Schema.String,
}

schema Inner {
	id: Schema.String,
}

// Not convertible: a second (annotations) argument.
export class WithAnnotations extends Schema.Class<WithAnnotations>("WithAnnotations")(
	{ n: Schema.Number },
	{ title: "WithAnnotations" }
) {}

// Not convertible: tagged error variant.
export class MyError extends Schema.TaggedError<MyError>("MyError")("MyError", {
	reason: Schema.String,
}) {}

export const Plain = Schema.Struct({ a: Schema.String });
