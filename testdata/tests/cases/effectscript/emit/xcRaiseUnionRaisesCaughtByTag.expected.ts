// raises A | B declaration raising two distinct tagged errors, each caught by tag
// (SPEC §5 raises, §6 catch matching)
import * as Effect from "effect/Effect";
import * as Data from "effect/Data";
class NotFound extends Data.TaggedError("NotFound")<{
    id: string;
}> {
}
class Timeout extends Data.TaggedError("Timeout")<{
    ms: number;
}> {
}
declare const lookupRow: (id: string) => any;
// the producer can raise either tagged error depending on input
const load = Effect.fn("load")(function* (id: string): Effect.fn.Return<string, NotFound | Timeout> {
    if (id === "")
        return yield* Effect.fail(new NotFound({ id }));
    const row = yield* lookupRow(id);
    if (row === undefined)
        return yield* Effect.fail(new Timeout({ ms: 5000 }));
    return String(row);
});
// the consumer catches each tag in its own arm with an `as e` binding
const loadOrDefault = Effect.fn("loadOrDefault")(function* (id: string): Effect.fn.Return<string> {
    const value = yield* load(id).pipe(Effect.catchTag("NotFound", (e) => Effect.gen(function* () { return `missing:${e.id}`; })), Effect.catchTag("Timeout", (e) => Effect.gen(function* () { return `slow:${e.ms}`; })));
    return value;
});
