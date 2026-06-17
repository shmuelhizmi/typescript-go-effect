// destructuring bind forms — renames, defaults, nesting, array/object mix,
// rest elements — each must lower to `const <pattern> = yield* <rhs>`.

declare const pair: any;
declare const record: any;
declare const nested: any;
declare const tuple: any;

effect destructuring(): number {
  // object bind with rename + default
  { x: px, y: py = 10 } <- record

  // array bind with hole + rest
  [, second, ...others] <- tuple

  // nested object/array destructuring
  { outer: { inner = 1 }, list: [head] } <- nested

  // simple swap-style two-element array bind
  [left, right] <- pair

  return px + py + second + others.length + inner + head + left + right
}
