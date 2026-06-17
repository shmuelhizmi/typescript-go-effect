// a layer whose body accesses another service before returning its impl
// (inter-layer dependency: Logger <- Logger inside the layer body)
import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";
import * as Context from "effect/Context";
class Logger extends Context.Tag("Logger")<Logger, {
    info(msg: string): void;
}>() {
}
class Greeter extends Context.Tag("Greeter")<Greeter, {
    greet(name: string): string;
}>() {
}
const GreeterLive = Layer.effect(Greeter, Effect.gen(function* () {
    const log = yield* Logger;
    return {
        greet: (name: string) => {
            log.info("greeting " + name);
            return "hi " + name;
        },
    };
}));
