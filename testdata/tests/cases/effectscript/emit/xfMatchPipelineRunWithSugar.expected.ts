// multi-stage |> pipeline ending in a runner, plus Effect type sugar in a signature
import * as Effect from "effect/Effect";
declare class Boom {
    readonly _tag: "Boom";
}
declare class Clock {
    readonly _tag: "Clock";
}
declare const ServicesLive: any;
declare const withRetry: (n: number) => (e: any) => any;
declare function build(seed: number): Effect.Effect<number, Boom, Clock>;
const job = Effect.fn("job")(function* (seed: number): Effect.fn.Return<number, Boom, Clock> {
    const out = yield* build(seed);
    return out * 2;
});
Effect.runFork(Effect.provide(ServicesLive)(withRetry(3)(job(7))));
