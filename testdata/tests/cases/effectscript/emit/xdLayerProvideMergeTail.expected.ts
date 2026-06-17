// provide composition in the run tail: merge two layers, then provide them,
// chained off a runtime call (SPEC §7.2 inline provide composition)
import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";
import * as Context from "effect/Context";
class Clock extends Context.Tag("Clock")<Clock, {
    now(): number;
}>() {
}
class Logger extends Context.Tag("Logger")<Logger, {
    info(msg: string): void;
}>() {
}
declare const runPromise: (e: unknown) => Promise<unknown>;
const ClockLive = Layer.succeed(Clock, { now: () => 0 });
const LoggerLive = Layer.succeed(Logger, { info: () => undefined });
const main = Effect.fn("main")(function* (): Effect.fn.Return<number, never, Clock | Logger> {
    const clock = yield* Clock;
    const logger = yield* Logger;
    logger.info("tick");
    return clock.now();
});
runPromise(Effect.provide(Layer.merge(ClockLive, LoggerLive))(main()));
