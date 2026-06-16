// |> pipeline (SPEC §10; TRANSPILATION §9)
import * as Effect from "effect/Effect";
declare const program: any;
declare const MainLive: any;
declare const double: (n: number) => number;
declare const add: (m: number) => (n: number) => number;
const a = double(1);
const b = add(2)(1);
const c = double(add(2)(double(1)));
Effect.runPromise(Effect.provide(MainLive)(program));
const fetchThing = Effect.fn("fetchThing")(function* (id: string) {
    const thing = yield* program;
    return thing;
});
