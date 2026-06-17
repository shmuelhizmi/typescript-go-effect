// using with a release body that binds, both one- and two-param release (SPEC §9)
import * as Effect from "effect/Effect";
declare const acquire: any;
declare const logClose: any;
const xeConcReleaseBinds = Effect.fn("xeConcReleaseBinds")(function* () {
    const a = yield* Effect.acquireRelease(acquire(), (c) => Effect.gen(function* () {
        const closed = yield* c.close();
        yield* logClose(closed);
    }));
    const b = yield* Effect.acquireRelease(acquire(), (res, exit) => Effect.gen(function* () {
        const flushed = yield* res.flush(exit);
        return flushed;
    }));
    return [a, b];
});
