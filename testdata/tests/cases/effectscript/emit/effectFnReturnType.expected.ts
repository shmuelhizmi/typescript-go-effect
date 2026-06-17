// An effect fn's `: A raises E requires R` return clause lowers to the
// generator's own return type `Effect.fn.Return<A, E, R>` (the type Effect.fn
// expects on its body). Omitted clauses default to never via the arg-count
// mapping: `: A` -> <A>, `: A raises E` -> <A, E>, `: A requires R` ->
// <A, never, R>. Also covers `raise (<- e)` — a raised parenthesized bind.
import { Effect } from "effect";
declare const lookup: (id: string) => Effect.Effect<string, NotFound, Db>;
declare const Db: Effect.Effect<Db>;
declare class NotFound {
    _tag: "NotFound";
}
declare class Db {
}
const full = Effect.fn("full")(function* (id: string): Effect.fn.Return<string, NotFound, Db> {
    const db = yield* Db;
    return yield* lookup(id);
});
const onlyRaises = Effect.fn("onlyRaises")(function* (id: string): Effect.fn.Return<string, NotFound> {
    return yield* lookup(id);
});
const onlyRequires = Effect.fn("onlyRequires")(function* (id: string): Effect.fn.Return<number, never, Db> {
    const db = yield* Db;
    return 1;
});
const plain = Effect.fn("plain")(function* (id: string): Effect.fn.Return<string> {
    return id;
});
const rethrow = Effect.fn("rethrow")(function* (id: string): Effect.fn.Return<string, NotFound> {
    return yield* Effect.fail((yield* lookup(id)));
});
