// `A raises E requires R` type sugar (SPEC §3.2, GRAMMAR §5)

declare class Boom { readonly _tag: "Boom" }
declare class Db { readonly _tag: "Db" }

type Full = number raises Boom requires Db;
type NoReq = string raises Boom;
type NoErr = string requires Db;
type Grouped = (string | number) raises Boom;

effect f(): number raises Boom requires Db {
  return 1
}

declare function takes(e: number raises Boom): void;
