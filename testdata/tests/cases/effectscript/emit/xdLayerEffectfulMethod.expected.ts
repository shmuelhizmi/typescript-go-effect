// a service whose method is itself an effect with parameters, consumed via `<-`
// (method return uses `raises` sugar -> Effect.Effect<...>; called result is yielded)
import * as Effect from "effect/Effect";
import * as Context from "effect/Context";
declare class NotFound {
    readonly _tag: "NotFound";
}
class UserRepo extends Context.Tag("UserRepo")<UserRepo, {
    findById(id: string): Effect.Effect<string, NotFound, never>;
}>() {
}
const loadName = Effect.fn("loadName")(function* (id: string): Effect.fn.Return<string, never, UserRepo> {
    const repo = yield* UserRepo;
    const name = yield* repo.findById(id);
    return name.toUpperCase();
});
