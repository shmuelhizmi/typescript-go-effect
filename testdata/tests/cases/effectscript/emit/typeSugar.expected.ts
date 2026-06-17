// `A raises E requires R` type sugar (SPEC §3.2, GRAMMAR §5)
import * as Effect from "effect/Effect";
declare class Boom {
    readonly _tag: "Boom";
}
declare class Db {
    readonly _tag: "Db";
}
type Full = Effect.Effect<number, Boom, Db>;
type NoReq = Effect.Effect<string, Boom, never>;
type NoErr = Effect.Effect<string, never, Db>;
type Grouped = Effect.Effect<(string | number), Boom, never>;
const f = Effect.fn("f")(function* (): Effect.fn.Return<number, Boom, Db> {
    return 1;
});
declare function takes(e: Effect.Effect<number, Boom, never>): void;
