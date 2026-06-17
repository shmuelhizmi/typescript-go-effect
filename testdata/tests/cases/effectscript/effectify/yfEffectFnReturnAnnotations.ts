import { Effect } from "effect";

declare const run: Effect.Effect<string, never, never>;
declare class DbError {}
declare class Database {}
declare class NotFound {}

// Return-annotated Effect.fn — the spring house style — reverses to the
// `effect name(params): A raises E requires R { … }` clause. The arg-count of
// Effect.fn.Return maps back exactly: <A,E,R> -> raises+requires, <A,E> ->
// raises only, <A,never,R> -> requires only, <A> -> bare.
export const getUser = Effect.fn("getUser")(function* (id: string): Effect.fn.Return<string, DbError, Database> {
	return yield* run;
});

export const validate = Effect.fn("validate")(function* (id: string): Effect.fn.Return<string, NotFound> {
	return yield* run;
});

export const load = Effect.fn("load")(function* (id: string): Effect.fn.Return<string, never, Database> {
	return yield* run;
});

export const echo = Effect.fn("echo")(function* (id: string): Effect.fn.Return<string> {
	return yield* run;
});

// A generator return annotation that is not Effect.fn.Return has no clause to
// reverse into, so the whole declaration stays verbatim.
export const opaque = Effect.fn("opaque")(function* (id: string): Generator<never, string, never> {
	return yield* run;
});
