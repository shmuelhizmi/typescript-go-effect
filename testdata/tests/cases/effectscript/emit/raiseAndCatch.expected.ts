// raise + postfix catch arms (SPEC §5–6, TRANSPILATION §3–4)
import * as Effect from "effect/Effect";
import { Data } from "effect";
class NotFound extends Data.TaggedError("NotFound")<{
    id: string;
}> {
}
class DbError extends Data.TaggedError("DbError")<{}> {
}
class NetError extends Data.TaggedError("NetError")<{}> {
}
class HttpError extends Data.TaggedError("HttpError")<{
    status: number;
    cause?: unknown;
}> {
}
declare const renderUser: (id: string) => any;
declare const render404: (id: string) => string;
const raises = Effect.fn("raises")(function* (id: string) {
    if (id === "")
        return yield* Effect.fail(new HttpError({ status: 400 }));
    const v = id !== "x" ? id : (yield* Effect.fail(new NotFound({ id })));
    return yield* Effect.die(new Error("boom"));
});
const catches = Effect.fn("catches")(function* (id: string): Effect.fn.Return<string> {
    const html = yield* renderUser(id).pipe(Effect.catchTag("NotFound", (e) => Effect.gen(function* () { return render404(e.id); })), ((h) => Effect.catchTags({ DbError: h, NetError: h }))((e) => Effect.gen(function* () { return (yield* Effect.fail(new HttpError({ status: 503, cause: e }))); })), Effect.catchAll((e) => Effect.gen(function* () { yield* Effect.logError(e); return "oops"; })));
    return html;
});
const regionGuard = Effect.fn("regionGuard")(function* (id: string): Effect.fn.Return<string> {
    const page = yield* Effect.gen(function* () {
        const user = yield* renderUser(id);
        return String(user);
    }).pipe(Effect.catchTag("NotFound", (_) => Effect.gen(function* () { return "missing"; })));
    return page;
});
