// raise.die (defect) vs raise (typed failure) side by side, in both statement
// and expression position (SPEC §5.2 raise vs raise.die)
import * as Effect from "effect/Effect";
import * as Data from "effect/Data";
class Invalid extends Data.TaggedError("Invalid")<{
    field: string;
}> {
}
declare const parsePort: (raw: string) => number;
const validate = Effect.fn("validate")(function* (raw: string) {
    // statement position: typed failure
    if (raw === "")
        return yield* Effect.fail(new Invalid({ field: "port" }));
    // statement position: unrecoverable defect
    if (raw === "panic")
        return yield* Effect.die(new Error("unreachable config"));
    const port = yield* parsePort(raw);
    // expression position: defect on one branch, typed failure on the other
    const checked = port > 0 ? port : (port < -1 ? (yield* Effect.die(new Error("negative"))) : (yield* Effect.fail(new Invalid({ field: "range" }))));
    return checked;
});
