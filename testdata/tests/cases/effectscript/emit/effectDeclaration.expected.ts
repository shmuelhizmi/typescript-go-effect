import { Effect } from "effect";

declare class Boom { readonly _tag: "Boom" }
declare class Db { readonly _tag: "Db" }

const plain = Effect.fn("plain")(function* () {
    return 1;
});

const annotated = Effect.fn("annotated")(function* () {
    return 2;
});

const sugared = Effect.fn("sugared")(function* (n: number) {
    return n;
});

const onlyRaises = Effect.fn("onlyRaises")(function* () {
});

export const exported = Effect.fn("exported")(function* () {
    return 3;
});

const entry = Effect.fn("entry")(function* () {
});
export default entry;

const anon = Effect.fn(function* (n: number) {
    return n * 2;
});

const block = Effect.gen(function* () {
    const x = yield* plain();
    return x + 1;
});
