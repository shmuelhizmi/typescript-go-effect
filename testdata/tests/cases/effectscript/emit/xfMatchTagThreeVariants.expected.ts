// match tag over a union of 3 tagged variants, returned from an effect
import * as Effect from "effect/Effect";
import * as Match from "effect/Match";
import { Data } from "effect";
class Circle extends Data.TaggedClass("Circle")<{
    r: number;
}> {
}
class Square extends Data.TaggedClass("Square")<{
    side: number;
}> {
}
class Rect extends Data.TaggedClass("Rect")<{
    w: number;
    h: number;
}> {
}
declare const shape: Circle | Square | Rect;
declare const measure: (a: number) => any;
const area = Effect.fn("area")(function* (): Effect.fn.Return<number> {
    const a = (yield* Match.value(shape).pipe(Match.tag("Circle", (c) => Effect.gen(function* () { return c.r * c.r * 3; })), Match.tag("Square", (s) => Effect.gen(function* () { return s.side * s.side; })), Match.tag("Rect", (r) => Effect.gen(function* () { return r.w * r.h; })), Match.exhaustive));
    const scaled = yield* measure(a);
    return Number(scaled);
});
