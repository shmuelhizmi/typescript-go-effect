import { Effect } from "effect";

declare class Boom { readonly _tag: "Boom" }
declare class Db { readonly _tag: "Db" }

type Full = Effect.Effect<number, Boom, Db>;
type NoReq = Effect.Effect<string, Boom, never>;
type NoErr = Effect.Effect<string, never, Db>;
type Grouped = Effect.Effect<string | number, Boom, never>;

const f = Effect.fn("f")(function* () {
    return 1;
});

declare function takes(e: Effect.Effect<number, Boom, never>): void;
