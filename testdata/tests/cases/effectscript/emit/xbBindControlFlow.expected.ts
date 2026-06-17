// binds (<-) threaded through every control-flow form — if/else, for-of,
// while, switch — confirming each branch lowers to yield* and the surrounding
// control flow survives the gen-function lowering.
import * as Effect from "effect/Effect";
declare const fetchN: any;
declare const items: number[];
declare function step(n: number): any;
declare function done(n: number): any;
const controlFlow = Effect.fn("controlFlow")(function* (flag: boolean) {
    let total = 0;
    if (flag) {
        const a = yield* fetchN;
        total = total + a;
    }
    else {
        const b = yield* fetchN;
        total = total + b;
    }
    for (const it of items) {
        const chunk = yield* step(it);
        total = total + chunk;
    }
    while (total < 100) {
        const inc = yield* fetchN;
        total = total + inc;
    }
    switch (total % 2) {
        case 0:
            const even = yield* done(total);
            total = total + even;
            break;
        default:
            const odd = yield* done(total);
            total = total + odd;
    }
    return total;
});
