// |> pipeline + decorator combinators (SPEC §3.5, §10; TRANSPILATION §1.2, §9)
import { Effect } from "effect";
import { Schedule } from "effect";
declare const program: any;
declare const MainLive: any;
declare const double: (n: number) => number;
declare const add: (m: number) => (n: number) => number;
const a = double(1);
const b = add(2)(1);
const c = double(add(2)(double(1)));
Effect.runPromise(Effect.provide(MainLive)(program));
const fetchThing = Effect.fn("fetchThing", Effect.timeout("5 seconds"), Effect.retry(Schedule.exponential("100 millis")))(function* (id: string) {
    const thing = yield* program;
    return thing;
});
