// bind forms (SPEC §4, TRANSPILATION §2) — binds are bare and immutable
import { Effect } from "effect";
declare const ea: any, eb: any, ec: any;
const binds = Effect.fn("binds")(function* () {
    const a = yield* ea;
    const { x, y } = yield* ec;
    const [first] = yield* ea;
    const typed: number = yield* eb;
    yield* ec;
    let mutable = 0;
    mutable = (yield* ea);
    const sum = (yield* ea) + (yield* eb);
    return sum + a + first + typed + x + y + mutable;
});
