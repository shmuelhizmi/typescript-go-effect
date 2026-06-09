import { Effect, Fiber as Fiber_1 } from "effect";

declare const ea: any, eb: any, ec: any;
declare const Fiber: any;

const concurrency = Effect.fn("concurrency")(function* () {
    const [a, b] = yield* Effect.all([ea, eb], { concurrency: "unbounded" });
    const named = yield* Effect.all({ left: ea, right: eb }, { concurrency: "unbounded" });
    const bounded = yield* Effect.all([ea, eb, ec], { concurrency: 4 });
    const winner = yield* Effect.race(ea, eb);
    const first3 = yield* Effect.raceAll([ea, eb, ec]);

    const fiber = yield* Effect.fork(ea);
    yield* Effect.addFinalizer(() => Effect.gen(function* () {
        yield* Fiber.interrupt(fiber);
    }));
    const joined = yield* Fiber_1.join(fiber);

    return [a, b, named, bounded, winner, first3, joined];
});

const resources = Effect.fn("resources")(function* () {
    const conn = yield* Effect.acquireRelease(ea, (c, exit) => Effect.gen(function* () {
        yield* c.close();
    }));
    yield* Effect.addFinalizer((exit) => Effect.gen(function* () {
        yield* ec;
    }));
    return conn;
});

// Note: the user's `Fiber` binding shadows the helper namespace, so the
// auto-import is aliased (`Fiber as Fiber_1`) — same collision rule as the
// react-jsx transform's `_jsx` helpers.
