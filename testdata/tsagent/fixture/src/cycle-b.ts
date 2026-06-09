// Other half of the deliberate import cycle. Also hosts a deliberate
// `as` cast and a `!` non-null assertion (used by `analyze assertions`
// and `type coverage`).

import { PREFIX } from "./cycle-a";

export function formatLabel(name: string): string {
    const raw: unknown = `${PREFIX}:${name}`;
    const text = raw as string; // deliberate `as` cast
    const last = text.split(":").pop();
    return last!; // deliberate non-null assertion
}
