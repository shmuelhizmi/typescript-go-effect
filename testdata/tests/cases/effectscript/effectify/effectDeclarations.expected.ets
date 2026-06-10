// Effect.fn / Effect.gen in declaration, expression, class-field and
// export-default positions; calls with extra pipe-combinator arguments
// stay untouched (EffectScript has no syntax for them)
import { Effect, Schedule } from "effect";

declare const policy: Schedule.Schedule<unknown>;

export effect plain() {
  return 1;
}

effect withParams(n: number, label: string) {
  return `${label}: ${n}`;
}

const decorated = Effect.fn("decorated")(function* () {
  return 2;
}, Effect.retry(policy), Effect.uninterruptible);

const anon = effect (n: number) {
  return n * 2;
};

const renamed = effect other() {
  return 3;
};

class Repo {
  effect getById(id: string) {
    return id.length;
  }
  static effect ping() {
    return "pong";
  }
}

export default effect entry() {
  return (<- plain());
};
