import { Data } from "effect";

// The exact desugared `tagged error` shape reverses to the sugar: tag string ==
// class name, an object type-literal prop shape, and an empty class body.
export tagged error NotFound { id: string }

tagged error Timeout { ms: number; cause?: unknown }

// Empty prop shape is fine.
export tagged error Cancelled {}

// Non-matching shapes stay verbatim: a tag that differs from the class name…
export class Mislabeled extends Data.TaggedError("Other")<{ x: number }> {}

// …and a class with its own body members (no `tagged error` surface for those).
export class WithBody extends Data.TaggedError("WithBody")<{ n: number }> {
	get doubled() {
		return this.n * 2;
	}
}
