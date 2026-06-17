// catch arms whose handler binds (<-) before yielding, plus re-raising a
// DIFFERENT tagged error from inside an arm (SPEC §6 catch handler bodies)
import * as Effect from "effect/Effect";
import * as Data from "effect/Data";
class NotFound extends Data.TaggedError("NotFound")<{
    id: string;
}> {
}
class Backend extends Data.TaggedError("Backend")<{
    cause: string;
}> {
}
declare const fetchRow: (id: string) => any;
declare const auditMiss: (id: string) => any;
declare const reportRow: (row: unknown) => string;
const resolve = Effect.fn("resolve")(function* (id: string) {
    const out = yield* fetchRow(id).pipe(
    // arm body binds via <- before returning a recovered value
    Effect.catchTag("NotFound", (e) => Effect.gen(function* () {
        const logged = yield* auditMiss(e.id);
        return `recovered:${logged}`;
    })), 
    // arm body binds, then re-raises a DIFFERENT tagged error
    Effect.catchTag("Backend", (e) => Effect.gen(function* () {
        const detail = yield* reportRow(e.cause);
        return yield* Effect.fail(new NotFound({ id: detail }));
    })));
    return out;
});
