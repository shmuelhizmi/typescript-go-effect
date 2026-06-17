import * as Effect from "effect/Effect";
import * as STM from "effect/STM";
// atomic{} used directly as a bound expression: STM.commit(atomic{ ... }) on
// the right of a `<-` bind inside an effect{}, including an inline atomic whose
// own body binds and raises on the STM channel, then a second committed inline
// atomic feeding the return.
import * as TRef from "effect/TRef";
declare const counter: any; // TRef<number>
declare const limit: any; // TRef<number>
const bump = Effect.gen(function* () {
    // inline atomic as the bound expression — its raise stays STM.fail
    const next = yield* STM.commit(STM.gen(function* () {
        const cur = yield* TRef.get(counter);
        const cap = yield* TRef.get(limit);
        if (cur >= cap)
            return yield* STM.fail(new Error("at capacity"));
        yield* TRef.set(counter, cur + 1);
        return cur + 1;
    }));
    if (next < 0)
        return yield* Effect.fail(new Error("effect-channel underflow"));
    // a second inline atomic committed and folded into the result
    const snapshot = yield* STM.commit(STM.gen(function* () {
        const v = yield* TRef.get(counter);
        return v;
    }));
    return next + snapshot;
});
