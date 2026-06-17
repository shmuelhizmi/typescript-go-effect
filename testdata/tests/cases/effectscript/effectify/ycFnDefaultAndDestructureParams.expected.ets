// Effect.fn string-name form whose params carry a default value and a
// destructuring pattern — the param list should survive verbatim into the
// `effect name(...)` declaration head.
import { Effect } from "effect";

declare const ea: Effect.Effect<number>;

// Default-valued param + object-destructured param, string-name form.
export effect fetchPage({ offset, limit }: { offset: number; limit: number },
  retries: number = 3) {
  base <- ea;
  return base + offset + limit + retries;
}
