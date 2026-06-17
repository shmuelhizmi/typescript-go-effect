// multi-stage |> pipeline ending in a runner, plus Effect type sugar in a signature

declare class Boom { readonly _tag: "Boom" }
declare class Clock { readonly _tag: "Clock" }

declare const ServicesLive: any;
declare const withRetry: (n: number) => (e: any) => any;

declare function build(seed: number): number raises Boom requires Clock;

effect job(seed: number): number raises Boom requires Clock {
  out <- build(seed)
  return out * 2
}

job(7)
  |> withRetry(3)
  |> Effect.provide(ServicesLive)
  |> Effect.runFork;
