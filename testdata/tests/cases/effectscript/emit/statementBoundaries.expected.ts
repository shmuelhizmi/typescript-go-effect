// Statement-boundary / ASI stress test: an expression statement of each shape
// immediately followed (newline-separated, no semicolons) by an EffectScript
// statement form. Catches the class where the preceding expression absorbs the
// next statement — e.g. `user.length` swallowing `[a, b] <- e` as element access.
//
// Note: tagged-template (`expr` ⏎ `` `t` ``) and call (`f()` ⏎ `(x)`) absorption
// are *standard JS ASI* and intentionally inherited — not exercised here.
import * as Effect from "effect/Effect";
declare const arr: number[];
declare function findUser(id: string): string;
declare function greet(name: string): string;
declare function doThing(): {
    len: number;
};
declare function notify(u: string): string;
declare const a: {
    b: {
        c: number;
    };
};
declare function acquire(): string;
const boundaries = Effect.fn("boundaries")(function* () {
    const user = yield* findUser("42");
    // member-access expr  then  array destructuring bind  (the reported bug)
    user.length;
    const [g1, g2] = yield* Effect.all([greet(user), greet("world")], { concurrency: "unbounded" });
    // call expr  then  object destructuring bind
    doThing();
    const { len } = yield* doThing();
    // member-chain expr  then  identifier bind
    a.b.c;
    const next = yield* findUser(user);
    // element-access expr  then  typed bind
    arr[0];
    const count: number = yield* doThing().len;
    // member expr  then  discard bind
    user.length;
    yield* notify(user);
    // member expr  then  raise (in a never-taken branch)
    user.length;
    if (user === "")
        return yield* Effect.fail(new Error("empty"));
    // binary expr  then  defer
    user.length + arr.length;
    yield* Effect.addFinalizer(() => Effect.gen(function* () { yield* notify("cleanup"); }));
    // member expr  then  using-bind with release
    arr.length;
    const conn = yield* Effect.acquireRelease(acquire(), (c) => Effect.gen(function* () { yield* notify(c); }));
    // member expr  then  pipeline continuation on the next line
    user.length;
    ((s: string) => s.toUpperCase())(greet(user));
    // genuine element access across a newline (NO bind) must stay element access
    const first = arr[0];
    return `${g1} | ${g2} | ${next} | ${count} | ${len} | ${conn} | ${first}`;
});
boundaries();
