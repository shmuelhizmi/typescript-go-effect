import { Data } from "effect";
import { Effect, Match } from "effect";

class NotFound extends Data.TaggedError("NotFound")<{ id: string }> {}
class DbError extends Data.TaggedError("DbError")<{}> {}
class NetError extends Data.TaggedError("NetError")<{}> {}

declare const res: { status: number; body?: string };
declare const error: NotFound | DbError | NetError;

const label = Match.value(res).pipe(
    Match.when({ status: 200 }, ({ body }) => body),
    Match.whenOr({ status: 301 }, { status: 302 }, () => "redirect"),
    Match.when((v) => v.status >= 500, ({ status: s }) => `server error ${s}`),
    Match.orElse(() => "unknown"),
);

const sameAsAbove = Match.value(res).pipe(
    Match.when({ status: 200 }, () => "ok"),
    Match.orElse(() => "other"),
);

const msg = Match.value(error).pipe(
    Match.tag("NotFound", (e) => `missing ${e.id}`),
    Match.tags({ DbError: () => "infra down", NetError: () => "infra down" }),
    Match.exhaustive,
);

declare const fetchFallback: (id: string) => any;

const effectful = Effect.fn("effectful")(function* (id: string) {
    const out = (yield* Match.value(error).pipe(
        Match.tag("NotFound", (e) => Effect.gen(function* () {
            const v = yield* fetchFallback(e.id);
            return v;
        })),
        Match.orElse(() => Effect.succeed("n/a")),
    ));
    return out;
});
