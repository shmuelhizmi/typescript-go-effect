// match expressions: value mode + tag mode (SPEC §11, TRANSPILATION §10)
import * as Effect from "effect/Effect";
import * as Match from "effect/Match";
import { Data } from "effect";
class NotFound extends Data.TaggedError("NotFound")<{
    id: string;
}> {
}
class DbError extends Data.TaggedError("DbError")<{}> {
}
class NetError extends Data.TaggedError("NetError")<{}> {
}
declare const res: {
    status: number;
    body?: string;
};
declare const error: NotFound | DbError | NetError;
const label = Match.value(res).pipe(Match.when({ status: 200 }, ({ body }) => body), Match.when({ status: 301 }, (_) => "redirect"), Match.when((v) => (({ status: s }) => s >= 500)(v), ({ status: s }) => `server error ${s}`), Match.orElse((_) => "unknown"));
const sameAsAbove = Match.value(res).pipe(Match.when({ status: 200 }, (_) => "ok"), Match.orElse((_) => "other"));
const msg = Match.value(error).pipe(Match.tag("NotFound", (e) => `missing ${e.id}`), ((h) => Match.tags({ DbError: h, NetError: h }))((_) => "infra down"), Match.exhaustive);
declare const fetchFallback: (id: string) => any;
const effectful = Effect.fn("effectful")(function* (id: string) {
    const out = (yield* Match.value(error).pipe(Match.tag("NotFound", (e) => Effect.gen(function* () { const v = yield* fetchFallback(e.id); return v; })), Match.orElse((_) => Effect.gen(function* () { return "n/a"; }))));
    return out;
});
