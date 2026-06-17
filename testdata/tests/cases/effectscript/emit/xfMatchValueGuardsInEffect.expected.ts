// match value mode: multiple literal arms + guards + `_` default, bound in an effect
import * as Effect from "effect/Effect";
import * as Match from "effect/Match";
declare const classify: (n: number) => any;
const grade = Effect.fn("grade")(function* (score: number): Effect.fn.Return<string> {
    const tier = (yield* Match.value(score).pipe(Match.whenOr(0, 1, 2, (_) => Effect.gen(function* () { return "low"; })), Match.when((v) => ((n) => n >= 90)(v), (n) => Effect.gen(function* () { return "excellent"; })), Match.when((v) => ((n) => n >= 50)(v), (n) => Effect.gen(function* () { return "pass"; })), Match.orElse((_) => Effect.gen(function* () { return "fail"; }))));
    const detail = yield* classify(score);
    return `${tier}:${String(detail)}`;
});
