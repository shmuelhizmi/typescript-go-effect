import * as Effect from "effect/Effect";
import * as STM from "effect/STM";
// STM-specific ops inside one atomic body: STM.retry as a bound statement,
// several TRef reads/writes, a conditional STM.fail (raise) and a conditional
// STM.die (raise.die). Binds stay yield*; both raise forms pick the STM channel.
import * as TRef from "effect/TRef";
declare const supply: any; // TRef<number>
declare const demand: any; // TRef<number>
declare const filled: any; // TRef<number>
const match = STM.gen(function* () {
    const available = yield* TRef.get(supply);
    const wanted = yield* TRef.get(demand);
    if (available === 0)
        yield* STM.retry;
    if (wanted < 0)
        return yield* STM.die(new Error("corrupt demand"));
    if (wanted > available)
        return yield* STM.fail(new Error("oversold"));
    yield* TRef.set(supply, available - wanted);
    yield* TRef.update(filled, (n: number) => n + wanted);
    const remaining = yield* TRef.get(supply);
    return remaining;
});
// commit the STM transaction from an effect{} so the lowering still imports
// effect/Effect (and the migrator can round-trip it).
const run = Effect.gen(function* () {
    const left = yield* STM.commit(match);
    return left;
});
