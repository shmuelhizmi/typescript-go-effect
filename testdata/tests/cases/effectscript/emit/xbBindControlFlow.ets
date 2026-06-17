// binds (<-) threaded through every control-flow form — if/else, for-of,
// while, switch — confirming each branch lowers to yield* and the surrounding
// control flow survives the gen-function lowering.

declare const fetchN: any;
declare const items: number[];
declare function step(n: number): any;
declare function done(n: number): any;

effect controlFlow(flag: boolean): number {
  let total = 0

  if (flag) {
    a <- fetchN
    total = total + a
  } else {
    b <- fetchN
    total = total + b
  }

  for (const it of items) {
    chunk <- step(it)
    total = total + chunk
  }

  while (total < 100) {
    inc <- fetchN
    total = total + inc
  }

  switch (total % 2) {
    case 0:
      even <- done(total)
      total = total + even
      break
    default:
      odd <- done(total)
      total = total + odd
  }

  return total
}
