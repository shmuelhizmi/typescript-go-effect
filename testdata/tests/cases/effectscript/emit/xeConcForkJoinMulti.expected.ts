// multiple forks stored as fibers and joined later (SPEC §8)
import * as Effect from "effect/Effect";
import * as Fiber from "effect/Fiber";
declare const task1: any, task2: any, task3: any;
const xeConcForkJoinMulti = Effect.fn("xeConcForkJoinMulti")(function* () {
    const f1 = yield* Effect.fork(task1);
    const f2 = yield* Effect.fork(task2);
    const f3 = yield* Effect.fork(task3);
    const a = yield* Fiber.join(f1);
    const b = yield* Fiber.join(f2);
    const c = yield* Fiber.join(f3);
    return [a, b, c];
});
