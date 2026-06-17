import { Effect } from "effect";

declare const run: Effect.Effect<string, never, never>;
declare class DbError {}
declare class Database {}
declare class NotFound {}

// Return-annotated Effect.fn — the spring house style — reverses to the
// `effect name(params): A raises E requires R { … }` clause. The arg-count of
// Effect.fn.Return maps back exactly: <A,E,R> -> raises+requires, <A,E> ->
// raises only, <A,never,R> -> requires only, <A> -> bare.
export effect getUser(id: string): string raises DbError requires Database {
	return (<- run);
}

export effect validate(id: string): string raises NotFound {
	return (<- run);
}

export effect load(id: string): string requires Database {
	return (<- run);
}

export effect echo(id: string): string {
	return (<- run);
}

// A generator return annotation that is not Effect.fn.Return has no clause to
// reverse into, so the whole declaration stays verbatim.
export const opaque = Effect.fn("opaque")(function* (id: string): Generator<never, string, never> {
	return yield* run;
});
