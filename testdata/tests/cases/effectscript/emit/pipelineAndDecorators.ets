// |> pipeline + decorator combinators (SPEC §3.5, §10; TRANSPILATION §1.2, §9)

import { Schedule } from "effect";

declare const program: any;
declare const MainLive: any;
declare const double: (n: number) => number;
declare const add: (m: number) => (n: number) => number;

const a = 1 |> double;
const b = 1 |> add(2);
const c = 1 |> double |> add(2) |> double;

program |> Effect.provide(MainLive) |> Effect.runPromise;

@retry(Schedule.exponential("100 millis"))
@timeout("5 seconds")
effect fetchThing(id: string) {
  thing <- program
  return thing
}
