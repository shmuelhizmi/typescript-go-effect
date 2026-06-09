// effect declarations: plain, annotated, sugar-annotated, export forms,
// expressions and blocks (SPEC §3, TRANSPILATION §1)

import { Effect } from "effect";

declare class Boom { readonly _tag: "Boom" }
declare class Db { readonly _tag: "Db" }

effect plain() {
  return 1
}

effect annotated(): Effect.Effect<number, Boom, Db> {
  return 2
}

effect sugared(n: number): number raises Boom requires Db {
  return n
}

effect onlyRaises(): void raises Boom {
}

export effect exported(): number {
  return 3
}

export default effect entry(): void {
}

const anon = effect (n: number) {
  return n * 2
};

const block = effect {
  x <- plain()
  return x + 1
};
