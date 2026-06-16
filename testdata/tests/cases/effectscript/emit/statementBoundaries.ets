// Statement-boundary / ASI stress test: an expression statement of each shape
// immediately followed (newline-separated, no semicolons) by an EffectScript
// statement form. Catches the class where the preceding expression absorbs the
// next statement — e.g. `user.length` swallowing `[a, b] <- e` as element access.
//
// Note: tagged-template (`expr` ⏎ `` `t` ``) and call (`f()` ⏎ `(x)`) absorption
// are *standard JS ASI* and intentionally inherited — not exercised here.

declare const arr: number[];
declare function findUser(id: string): string;
declare function greet(name: string): string;
declare function doThing(): { len: number };
declare function notify(u: string): string;
declare const a: { b: { c: number } };
declare function acquire(): string;

effect boundaries(): string {
  user <- findUser("42")

  // member-access expr  then  array destructuring bind  (the reported bug)
  user.length
  [g1, g2] <- par [greet(user), greet("world")]

  // call expr  then  object destructuring bind
  doThing()
  { len } <- doThing()

  // member-chain expr  then  identifier bind
  a.b.c
  next <- findUser(user)

  // element-access expr  then  typed bind
  arr[0]
  count: number <- doThing().len

  // member expr  then  discard bind
  user.length
  <- notify(user)

  // member expr  then  raise (in a never-taken branch)
  user.length
  if (user === "") raise new Error("empty")

  // binary expr  then  defer
  user.length + arr.length
  defer { <- notify("cleanup") }

  // member expr  then  using-bind with release
  arr.length
  using conn <- acquire() release (c) { <- notify(c) }

  // member expr  then  pipeline continuation on the next line
  user.length
  greet(user)
    |> ((s: string) => s.toUpperCase())

  // genuine element access across a newline (NO bind) must stay element access
  const first = arr
  [0]

  return `${g1} | ${g2} | ${next} | ${count} | ${len} | ${conn} | ${first}`
}

boundaries()
