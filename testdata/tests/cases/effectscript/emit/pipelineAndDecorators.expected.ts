import { Schedule } from "effect";
import { Effect, pipe } from "effect";

declare const program: any;
declare const MainLive: any;
declare const double: (n: number) => number;
declare const add: (m: number) => (n: number) => number;

const a = double(1);
const b = add(2)(1);
const c = pipe(1, double, add(2), double);

pipe(program, Effect.provide(MainLive), Effect.runPromise);

const fetchThing = Effect.fn(
    "fetchThing",
    Effect.timeout("5 seconds"),
    Effect.retry(Schedule.exponential("100 millis")),
)(function* (id: string) {
    const thing = yield* program;
    return thing;
});
