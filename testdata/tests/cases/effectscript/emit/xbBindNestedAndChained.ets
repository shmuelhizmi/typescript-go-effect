// bind whose RHS is a nested `effect { }` block expression, chained binds that
// feed each other, and discard binds in statement position.

declare const source: any;
declare function load(id: number): any;
declare function persist(v: number): any;

effect nestedAndChained(): number {
  // RHS is itself an inline effect{} block expression
  seed <- effect {
    raw <- source
    return raw + 1
  }

  // chained binds: each RHS depends on the previous binding
  first  <- load(seed)
  second <- load(first)
  third  <- load(second)

  // discard binds — no binding introduced
  <- persist(third)
  <- persist(first + second)

  return seed + first + second + third
}
