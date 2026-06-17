// An Effect.fn whose body has its own binds and embeds a nested Effect.gen
// that is itself yielded — both the outer fn and the inner gen must convert.
import { Effect } from "effect";

declare const ea: Effect.Effect<number>;
declare const eb: Effect.Effect<string>;

export effect aggregate(seed: number) {
  first <- ea;
  inner <- effect {
    label <- eb;
    return label.length + seed;
  };
  return first + inner;
}
