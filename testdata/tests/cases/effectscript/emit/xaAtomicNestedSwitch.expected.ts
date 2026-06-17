import * as Effect from "effect/Effect";
import * as STM from "effect/STM";
// Nested body kinds flip the raise channel: an effect{} nested inside an
// atomic{} switches raise back to Effect.fail, and an atomic{} nested inside
// an effect{} switches it to STM.fail — the innermost enclosing body wins.
import * as TRef from "effect/TRef";
declare const gate: any; // TRef<number>
// atomic outer, effect inner: outer raise -> STM.fail, inner raise -> Effect.fail
const outerAtomic = STM.gen(function* () {
    const level = yield* TRef.get(gate);
    if (level < 0)
        return yield* STM.fail(new Error("stm-channel"));
    const inner = yield* STM.commit(Effect.gen(function* () {
        if (level > 100)
            return yield* Effect.fail(new Error("effect-channel"));
        return level * 2;
    }));
    return inner;
});
// effect outer, atomic inner: outer raise -> Effect.fail, inner raise -> STM.fail
const outerEffect = Effect.gen(function* () {
    const seed = yield* TRef.get(gate);
    if (seed < 0)
        return yield* Effect.fail(new Error("effect-channel"));
    const doubled = yield* STM.commit(STM.gen(function* () {
        if (seed > 100)
            return yield* STM.fail(new Error("stm-channel"));
        return seed + seed;
    }));
    return doubled;
});
