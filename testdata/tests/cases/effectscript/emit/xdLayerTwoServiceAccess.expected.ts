// an effect that requires two services and accesses both inside the body
// (two distinct `x <- Service` binds + a method-call bind)
import * as Effect from "effect/Effect";
import * as Context from "effect/Context";
class Config extends Context.Tag("Config")<Config, {
    port(): number;
}>() {
}
class Logger extends Context.Tag("Logger")<Logger, {
    info(msg: string): void;
}>() {
}
const boot = Effect.fn("boot")(function* () {
    const config = yield* Config;
    const logger = yield* Logger;
    const p = yield* config.port();
    logger.info("listening on " + p);
    return "started:" + p;
});
