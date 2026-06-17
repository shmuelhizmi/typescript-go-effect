// schema with nested schema field + optional field, decoded inside an effect with a catch

import { Data } from "effect";

class ParseError extends Data.TaggedError("ParseError")<{ cause: unknown }> {}

schema Address {
  city: Schema.String
  zip:  Schema.optional(Schema.String)
}

schema Customer {
  name:    Schema.String
  address: Address
  vip:     Schema.optional(Schema.Boolean)
}

declare const input: unknown;

effect loadCustomer(): Customer raises ParseError {
  customer <- Schema.decodeUnknown(Customer)(input) catch {
    _ as e >> raise new ParseError({ cause: e })
  }
  return customer
}
