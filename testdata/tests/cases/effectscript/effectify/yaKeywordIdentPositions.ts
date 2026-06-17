// Contextual-keyword identifiers (service, match, join, release, layer) in
// non-keyword positions must not block conversion: binding names, returns,
// member calls and property reads are all harmless. A bare `join(x)` call is
// the one genuine hazard (it re-parses as the fork/join operator), so that
// body stays an Effect.gen.
import { Effect } from "effect";

declare const acquire: Effect.Effect<string>;
declare function build(parts: string[]): { match: number; release: () => void };

export const makeService = Effect.gen(function* () {
  const service = yield* acquire;
  const layer = service.length;
  const match = build([service]);
  const summary = match.match + service.length;
  const joined = [service].join(",");
  match.release();
  return { service, layer, summary, joined };
});

export const hazard = Effect.gen(function* () {
  const parts = yield* acquire;
  return join(parts);
});

declare function join(x: string): string;
