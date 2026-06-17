// bind followed by an early `return` inside a branch, binds nested in a
// try/catch, and a discard bind guarding a conditional return.
import * as Effect from "effect/Effect";
declare const probe: any;
declare function risky(n: number): any;
declare function cleanup(): any;
const earlyReturn = Effect.fn("earlyReturn")(function* (n: number): Effect.fn.Return<number> {
    const status = yield* probe;
    // early return out of a guarded branch, after a bind in that branch
    if (status < 0) {
        const fallback = yield* risky(0);
        return fallback;
    }
    // bind inside a try/catch; control flow + try survive lowering
    let acc = status;
    try {
        const value = yield* risky(n);
        acc = acc + value;
    }
    catch (e) {
        const recovered = yield* risky(-1);
        acc = acc + recovered;
    }
    finally {
        yield* cleanup();
    }
    return acc;
});
