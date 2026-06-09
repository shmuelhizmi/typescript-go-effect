import { Effect, Layer, Context } from "effect";

declare const cfg: { dbUrl: string };
declare const createPool: (cfg: unknown) => any;

class Database extends Context.Tag("Database")<Database, {
    query(sql: string): Effect.Effect<unknown[], DbError>;
    readonly url: string;
}>() {}

export class Cache extends Context.Tag("Cache")<Cache, {
    get(key: string): string | undefined;
}>() {}

declare class DbError { readonly _tag: "DbError" }
declare const PgPool: any;

const DatabaseLive = Layer.effect(Database, Effect.gen(function* () {
    const pool = yield* PgPool;
    return {
        query: (sql: string) => pool.query(sql),
        url: cfg.dbUrl,
    };
}));

const PgPoolLive = Layer.scoped(Database, Effect.gen(function* () {
    const pool = yield* Effect.acquireRelease(createPool(cfg), (p) => Effect.gen(function* () {
        yield* p.end();
    }));
    return pool;
}));

const CacheTest = Layer.succeed(Cache, { get: () => undefined });

const Wired = Layer.effect(Database, Effect.gen(function* () {
    return (yield* PgPool);
})).pipe(Layer.provide([CacheTest]));
