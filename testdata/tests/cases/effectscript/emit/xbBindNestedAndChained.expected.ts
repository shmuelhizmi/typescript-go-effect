// bind whose RHS is a nested `effect { }` block expression, chained binds that
// feed each other, and discard binds in statement position.
import * as Effect from "effect/Effect";
declare const source: any;
declare function load(id: number): any;
declare function persist(v: number): any;
const nestedAndChained = Effect.fn("nestedAndChained")(function* (): Effect.fn.Return<number> {
    // RHS is itself an inline effect{} block expression
    const seed = yield* Effect.gen(function* () {
        const raw = yield* source;
        return raw + 1;
    });
    // chained binds: each RHS depends on the previous binding
    const first = yield* load(seed);
    const second = yield* load(first);
    const third = yield* load(second);
    // discard binds — no binding introduced
    yield* persist(third);
    yield* persist(first + second);
    return seed + first + second + third;
});
