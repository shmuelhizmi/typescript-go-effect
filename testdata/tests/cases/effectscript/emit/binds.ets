// bind forms (SPEC §4, TRANSPILATION §2) — binds are bare and immutable

declare const ea: any, eb: any, ec: any;

effect binds() {
  a <- ea
  { x, y } <- ec
  [first] <- ea
  typed: number <- eb
  <- ec
  let mutable = 0
  mutable = (<- ea)
  const sum = (<- ea) + (<- eb)
  return sum + a + first + typed + x + y + mutable
}
