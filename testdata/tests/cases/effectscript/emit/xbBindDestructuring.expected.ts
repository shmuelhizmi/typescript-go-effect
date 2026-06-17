// destructuring bind forms — renames, defaults, nesting, array/object mix,
// rest elements — each must lower to `const <pattern> = yield* <rhs>`.
import * as Effect from "effect/Effect";
declare const pair: any;
declare const record: any;
declare const nested: any;
declare const tuple: any;
const destructuring = Effect.fn("destructuring")(function* () {
    // object bind with rename + default
    const { x: px, y: py = 10 } = yield* record;
    // array bind with hole + rest
    const [, second, ...others] = yield* tuple;
    // nested object/array destructuring
    const { outer: { inner = 1 }, list: [head] } = yield* nested;
    // simple swap-style two-element array bind
    const [left, right] = yield* pair;
    return px + py + second + others.length + inner + head + left + right;
});
