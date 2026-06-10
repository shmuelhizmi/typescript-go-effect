// Effect.fn / Effect.gen in declaration, expression, class-field and
// export-default positions; calls with extra pipe-combinator arguments
// stay untouched (EffectScript has no syntax for them)
import { Effect, Schedule } from "effect";

declare const policy: Schedule.Schedule<unknown>;

export const plain = Effect.fn("plain")(function* () {
  return 1;
});

const withParams = Effect.fn("withParams")(function* (n: number, label: string) {
  return `${label}: ${n}`;
});

const decorated = Effect.fn("decorated")(function* () {
  return 2;
}, Effect.retry(policy), Effect.uninterruptible);

const anon = Effect.fn(function* (n: number) {
  return n * 2;
});

const renamed = Effect.fn("other")(function* () {
  return 3;
});

class Repo {
  getById = Effect.fn("Repo.getById")(function* (id: string) {
    return id.length;
  });
  static ping = Effect.fn("Repo.ping")(function* () {
    return "pong";
  });
}

export default Effect.fn("entry")(function* () {
  return yield* plain();
});
