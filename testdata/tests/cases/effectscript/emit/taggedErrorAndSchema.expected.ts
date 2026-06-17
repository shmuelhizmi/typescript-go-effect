// tagged error / schema declarations (SPEC §5.1, §7.4; TRANSPILATION §1.4, §1.5)
import * as Effect from "effect/Effect";
import * as Data from "effect/Data";
import * as Schema from "effect/Schema";
class NotFound extends Data.TaggedError("NotFound")<{
    id: string;
}> {
}
class RateLimited extends Data.TaggedError("RateLimited")<{}> {
}
export class Forbidden extends Data.TaggedError("Forbidden")<{
    reason: string;
    code: number;
}> {
}
class Person extends Schema.Class<Person>("Person")({
    name: Schema.String,
    age: Schema.Number
}) {
}
export class Account extends Schema.Class<Account>("Account")({
    owner: Person,
    tags: Schema.Array(Schema.String),
}) {
}
// contextual keywords stay plain identifiers outside the declaration shapes
const tagged = 1;
const schema = (x: number) => x;
const error = schema(tagged);
const member = { tagged, error };
const lookup = Effect.fn("lookup")(function* (id: string): Effect.fn.Return<Person, NotFound> {
    if (id === "")
        return yield* Effect.fail(new NotFound({ id }));
    const decoded = yield* Schema.decodeUnknown(Person)({ name: id, age: 1 });
    return decoded;
});
