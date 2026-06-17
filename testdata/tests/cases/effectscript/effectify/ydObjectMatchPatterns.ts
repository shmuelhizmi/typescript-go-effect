import { Effect, Match } from "effect";

// Single Match.when object patterns reverse to `{ … } >>` arms — including the
// case where an arm value is itself a parenthesized object literal `({ … })`
// followed by another `{`-led arm (the arrow-vs-object parser disambiguation).
export const classify = (input: { kind: "a" | "b"; n: number }) =>
	Match.value(input).pipe(
		Match.when({ kind: "a" }, (s) => ({ tag: "first", n: s.n })),
		Match.when({ kind: "b" }, (s) => ({ tag: "second", n: s.n })),
		Match.exhaustive
	);

// A pure match nested in a plain arrow inside an effect body stays pure: the
// arrow body is not an effect body, so its arms must not be wrapped in gen.
export const pick = Effect.gen(function* () {
	const queue = yield* Effect.succeed({ mode: "prod" as "prod" | "dev", a: "x", b: "y" });
	const resolve = (q: typeof queue) =>
		Match.value(q).pipe(
			Match.when({ mode: "prod" }, (access) => access.a),
			Match.when({ mode: "dev" }, (access) => access.b),
			Match.exhaustive
		);
	return resolve(queue);
});

// whenOr stays scalar-only: an or-pattern of object literals is left verbatim
// (it does not round-trip), while scalar whenOr still reverses.
export const grade = (score: 0 | 1 | 2) =>
	Match.value(score).pipe(
		Match.whenOr(0, 1, (n) => `low:${n}`),
		Match.when(2, (n) => `high:${n}`),
		Match.exhaustive
	);
