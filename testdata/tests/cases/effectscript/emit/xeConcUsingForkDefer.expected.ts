// a using-resource inside an effect that also forks, plus both defer forms (SPEC §8–9)
import * as Effect from "effect/Effect";
import * as Fiber from "effect/Fiber";
declare const openPool: any, background: any, cleanup: any;
const xeConcUsingForkDefer = Effect.fn("xeConcUsingForkDefer")(function* () {
    const pool = yield* Effect.acquireRelease(openPool(), (p) => Effect.gen(function* () { yield* p.drain(); }));
    const worker = yield* Effect.fork(background(pool));
    yield* Effect.addFinalizer(() => Effect.gen(function* () { yield* cleanup(pool); }));
    yield* Effect.addFinalizer((exit) => Effect.gen(function* () { yield* cleanup(exit); }));
    const done = yield* Fiber.join(worker);
    return [pool, done];
});
