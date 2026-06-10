// Effect.fn / Effect.gen in declaration, expression, class-field and
// export-default positions, with decorator combinators
import { Effect, Schedule } from "effect";

declare const policy: Schedule.Schedule<unknown>;

export effect plain() {
  return 1;
}

effect withParams(n: number, label: string) {
  return `${label}: ${n}`;
}

@Effect.uninterruptible
@Effect.retry(policy)
effect decorated() {
  return 2;
}

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
