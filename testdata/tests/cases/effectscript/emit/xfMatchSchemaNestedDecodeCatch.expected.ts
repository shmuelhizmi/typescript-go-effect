// schema with nested schema field + optional field, decoded inside an effect with a catch
import * as Effect from "effect/Effect";
import * as Schema from "effect/Schema";
import { Data } from "effect";
class ParseError extends Data.TaggedError("ParseError")<{
    cause: unknown;
}> {
}
class Address extends Schema.Class<Address>("Address")({
    city: Schema.String,
    zip: Schema.optional(Schema.String)
}) {
}
class Customer extends Schema.Class<Customer>("Customer")({
    name: Schema.String,
    address: Address,
    vip: Schema.optional(Schema.Boolean)
}) {
}
declare const input: unknown;
const loadCustomer = Effect.fn("loadCustomer")(function* () {
    const customer = yield* Schema.decodeUnknown(Customer)(input).pipe(Effect.catchAll((e) => Effect.gen(function* () { return (yield* Effect.fail(new ParseError({ cause: e }))); })));
    return customer;
});
