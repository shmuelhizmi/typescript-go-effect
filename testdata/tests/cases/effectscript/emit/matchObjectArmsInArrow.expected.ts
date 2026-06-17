// Object-pattern match arms whose value is a parenthesized object literal
// `({ … })` followed by another `{`-led arm — the arrow-vs-object parser
// disambiguation must not let the next arm's `{` be eaten as a block body.
// Also: a pure match inside a plain arrow nested in an effect body stays pure
// (the concise arrow body is not an effect body).
import { Effect, Match } from "effect";
declare const input: {
    kind: "a" | "b";
    n: number;
};
const classify = (i: typeof input) => Match.value(i).pipe(Match.when({ kind: "a" }, (s) => ({ tag: "first", n: s.n })), Match.when({ kind: "b" }, (s) => ({ tag: "second", n: s.n })), Match.exhaustive);
const pick = Effect.gen(function* () {
    const queue = yield* Effect.succeed({ mode: "prod" as "prod" | "dev", a: "x", b: "y" });
    const resolve = (q: typeof queue) => Match.value(q).pipe(Match.when({ mode: "prod" }, (access) => access.a), Match.when({ mode: "dev" }, (access) => access.b), Match.exhaustive);
    return resolve(queue);
});
