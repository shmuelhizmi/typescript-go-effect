import * as Effect from "effect/Effect";
import * as STM from "effect/STM";
// atomic declaration form with multiple params + return type lowers to an
// arrow returning STM.gen; calling it yields an STM value that an enclosing
// effect{} commits with STM.commit.
import * as TRef from "effect/TRef";
declare const ledger: any;
// multi-param + explicit return type
const withdraw = (amount: number, fee: number) => STM.gen(function* () {
    const current = yield* TRef.get(ledger);
    const total = yield* STM.succeed(amount + fee);
    if (current < total)
        return yield* STM.die(new Error("insufficient"));
    yield* TRef.set(ledger, current - total);
    return total;
});
// call the atomic declaration and commit it from inside an effect{}
const settle = Effect.gen(function* () {
    const charged = yield* STM.commit(withdraw(40, 2));
    if (charged <= 0)
        return yield* Effect.fail(new Error("nothing charged"));
    return charged;
});
