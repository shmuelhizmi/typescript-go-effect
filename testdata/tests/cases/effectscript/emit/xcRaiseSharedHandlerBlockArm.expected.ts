// shared-handler arm (T1 | T2) with a multi-statement BLOCK body that binds,
// followed by a `_` default that re-raises (SPEC §6 catchTags shared handler)
import * as Effect from "effect/Effect";
import * as Data from "effect/Data";
class DbError extends Data.TaggedError("DbError")<{
    query: string;
}> {
}
class NetError extends Data.TaggedError("NetError")<{
    host: string;
}> {
}
class Fatal extends Data.TaggedError("Fatal")<{
    detail: string;
}> {
}
declare const runQuery: (sql: string) => any;
declare const retryHint: (e: unknown) => any;
const query = Effect.fn("query")(function* (sql: string): Effect.fn.Return<string> {
    const rows = yield* runQuery(sql).pipe(
    // one shared handler for two tags, with a block body that binds first
    ((h) => Effect.catchTags({ DbError: h, NetError: h })
    // catch-all default that escalates to a fatal defect-free typed failure
    )((e) => Effect.gen(function* () {
        const hint = yield* retryHint(e);
        return `degraded:${hint}`;
    })), 
    // catch-all default that escalates to a fatal defect-free typed failure
    Effect.catchAll((e) => Effect.gen(function* () {
        yield* Effect.logError(e);
        return yield* Effect.fail(new Fatal({ detail: "unhandled" }));
    })));
    return rows;
});
