import { Effect, Match } from "effect";

// Single Match.when object patterns reverse to `{ … } >>` arms — including the
// case where an arm value is itself a parenthesized object literal `({ … })`
// followed by another `{`-led arm (the arrow-vs-object parser disambiguation).
export const classify = (input: { kind: "a" | "b"; n: number }) =>
	match (input) {
	  { kind: "a" } as s >> ({ tag: "first", n: s.n })
	  { kind: "b" } as s >> ({ tag: "second", n: s.n })
	};

// A pure match nested in a plain arrow inside an effect body stays pure: the
// arrow body is not an effect body, so its arms must not be wrapped in gen.
export const pick = effect {
	queue <- Effect.succeed({ mode: "prod" as "prod" | "dev", a: "x", b: "y" });
	const resolve = (q: typeof queue) =>
		match (q) {
		  { mode: "prod" } as access >> access.a
		  { mode: "dev" } as access >> access.b
		};
	return resolve(queue);
};

// whenOr stays scalar-only: an or-pattern of object literals is left verbatim
// (it does not round-trip), while scalar whenOr still reverses.
export const grade = (score: 0 | 1 | 2) =>
	match (score) {
	  0 | 1 as n >> `low:${n}`
	  2 as n >> `high:${n}`
	};
