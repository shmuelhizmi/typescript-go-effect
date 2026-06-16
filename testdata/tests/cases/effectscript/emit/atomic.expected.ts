// atomic { } / atomic name() {} — STM transactions (SPEC §9, TRANSPILATION §13)
// raise inside atomic lowers to STM.fail/STM.die; binds stay yield*.
import * as Effect from "effect/Effect";
import * as STM from "effect/STM";
declare const balance: any; // TRef<number>
declare const other: any; // TRef<number>
// block expression
const moveOne = STM.gen(function* () {
    const current = yield* TRef.get(balance);
    if (current <= 0)
        return yield* STM.fail(new Error("insufficient"));
    yield* TRef.set(balance, current - 1);
    yield* TRef.update(other, (n: number) => n + 1);
    return current;
});
// declaration form
const transfer = (amount: number) => STM.gen(function* () {
    const from = yield* TRef.get(balance);
    if (from < amount)
        return yield* STM.die(new Error("overdraft"));
    yield* TRef.set(balance, from - amount);
    const to = yield* TRef.get(other);
    yield* TRef.set(other, to + amount);
    return amount;
});
// nested: effect body inside switches the raise channel back to Effect
const program = Effect.gen(function* () {
    const snapshot = yield* TRef.get(balance);
    const committed = yield* STM.commit(STM.gen(function* () {
        const v = yield* TRef.get(balance);
        if (v < 0)
            return yield* STM.fail(new Error("negative"));
        return v;
    }));
    if (committed < 0)
        return yield* Effect.fail(new Error("effect-channel"));
    return snapshot + committed;
});
