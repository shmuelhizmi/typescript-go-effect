import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";
import * as Context from "effect/Context";
import * as Fiber from "effect/Fiber";
import * as Match from "effect/Match";
import * as Data from "effect/Data";
class NotFound extends Data.TaggedError("NotFound")<{
    id: string;
}> {
}
class RateLimited extends Data.TaggedError("RateLimited")<{}> {
}
class Greeter extends Context.Tag("Greeter")<Greeter, {
    greeting: Effect.Effect<string>;
}>() {
}
const GreeterLive = Layer.effect(Greeter, Effect.gen(function* () {
    return { greeting: Effect.succeed("Hello") };
}));
const findUser = Effect.fn("findUser")(function* (id: string) {
    if (id === "")
        return yield* Effect.fail(new NotFound({ id }));
    return `user-${id}`;
});
const fetchGreeting = Effect.fn("fetchGreeting")(function* (name: string) {
    const greeter = yield* Greeter;
    const prefix = yield* greeter.greeting;
    return `${prefix}, ${name}!`;
});
const main = Effect.fn("main")(function* () {
    const user = yield* findUser("42").pipe(Effect.catchTag("NotFound", (e) => Effect.gen(function* () { return `fallback-${e.id}`; })), Effect.catchAll((_) => Effect.gen(function* () { return "anonymous"; })));
    const missing = yield* findUser("").pipe(Effect.catchTag("NotFound", (e) => Effect.gen(function* () { return "missing!"; })));
    const [g1, g2] = yield* Effect.all([fetchGreeting(user), fetchGreeting(missing)], { concurrency: "unbounded" });
    const fiber = yield* Effect.fork(fetchGreeting("forked"));
    const forked = yield* Fiber.join(fiber);
    yield* Effect.addFinalizer(() => Effect.gen(function* () { yield* Effect.log("finalized"); }));
    const kind = (yield* Match.value(user.length).pipe(Match.when(7, (_) => Effect.gen(function* () { return "seven"; })), Match.orElse((_) => Effect.gen(function* () { return "other"; }))));
    const tagged = (yield* Match.value(new NotFound({ id: "x" }) as NotFound | RateLimited).pipe(Match.tag("NotFound", (e) => Effect.gen(function* () { return `nf:${e.id}`; })), Match.tag("RateLimited", (_) => Effect.gen(function* () { return "rl"; })), Match.exhaustive));
    const doubled = ((n: number) => n * 2)(21);
    return [g1, g2, forked, kind, tagged, String(doubled)].join(" | ");
});
((p: Promise<string>) => p.then(console.log))(Effect.runPromise(Effect.provide(GreeterLive)(Effect.scoped(main()))));
