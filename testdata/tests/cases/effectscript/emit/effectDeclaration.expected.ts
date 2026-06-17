// effect declarations: plain, annotated, sugar-annotated, export forms,
// expressions and blocks (SPEC §3, TRANSPILATION §1)
import { Effect } from "effect";
declare class Boom {
    readonly _tag: "Boom";
}
declare class Db {
    readonly _tag: "Db";
}
const plain = Effect.fn("plain")(function* () {
    return 1;
});
const annotated = Effect.fn("annotated")(function* (): Effect.fn.Return<Effect.Effect<number, Boom, Db>> {
    return 2;
});
const sugared = Effect.fn("sugared")(function* (n: number): Effect.fn.Return<number, Boom, Db> {
    return n;
});
const onlyRaises = Effect.fn("onlyRaises")(function* (): Effect.fn.Return<void, Boom> {
});
export const exported = Effect.fn("exported")(function* (): Effect.fn.Return<number> {
    return 3;
});
export default Effect.fn("entry")(function* (): Effect.fn.Return<void> {
});
const anon = Effect.fn(function* (n: number) {
    return n * 2;
});
const block = Effect.gen(function* () {
    const x = yield* plain();
    return x + 1;
});
