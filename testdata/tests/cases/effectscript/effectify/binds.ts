// binds in all forms: identifier, destructuring, typed, discard, expression
import { Effect } from "effect";

declare const ea: Effect.Effect<number>;
declare const eb: Effect.Effect<{ a: number; b: string }>;
declare const ec: Effect.Effect<number[]>;
declare function cond(): boolean;

export const program = Effect.gen(function* () {
  const x = yield* ea;
  const { a, b } = yield* eb;
  const [first] = yield* ec;
  const typed: number = yield* ea;
  yield* ea;
  let y: number;
  y = (yield* ea) + (yield* ea);
  const sum = cond() ? yield* ea : 0;
  return x + a + first + typed + y + sum + b.length;
});
