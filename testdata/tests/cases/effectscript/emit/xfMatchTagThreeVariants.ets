// match tag over a union of 3 tagged variants, returned from an effect

import { Data } from "effect";

class Circle extends Data.TaggedClass("Circle")<{ r: number }> {}
class Square extends Data.TaggedClass("Square")<{ side: number }> {}
class Rect extends Data.TaggedClass("Rect")<{ w: number; h: number }> {}

declare const shape: Circle | Square | Rect;
declare const measure: (a: number) => any;

effect area(): number {
  const a = match tag (shape) {
    Circle as c >> c.r * c.r * 3
    Square as s >> s.side * s.side
    Rect as r   >> r.w * r.h
  }
  scaled <- measure(a)
  return Number(scaled)
}
