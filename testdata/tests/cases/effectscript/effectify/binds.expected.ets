// binds in all forms: identifier, destructuring, typed, discard, expression
import { Effect } from "effect";

declare const ea: Effect.Effect<number>;
declare const eb: Effect.Effect<{ a: number; b: string }>;
declare const ec: Effect.Effect<number[]>;
declare function cond(): boolean;

export const program = effect {
  x <- ea;
  { a, b } <- eb;
  [first] <- ec;
  typed: number <- ea;
  <- ea;
  let y: number;
  y = (<- ea) + (<- ea);
  const sum = cond() ? (<- ea) : 0;
  return x + a + first + typed + y + sum + b.length;
};
