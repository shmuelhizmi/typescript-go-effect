// par over three effects with a destructured result + a two-element race (SPEC §8)
import * as Effect from "effect/Effect";
declare const ea: any, eb: any, ec: any;
const xeConcParRaceDestructure = Effect.fn("xeConcParRaceDestructure")(function* () {
    const [first, second, third] = yield* Effect.all([ea, eb, ec], { concurrency: "unbounded" });
    const { left, right } = yield* Effect.all({ left: ea, right: eb }, { concurrency: "unbounded" });
    const winner = yield* Effect.race(ea, eb);
    return [first, second, third, left, right, winner];
});
