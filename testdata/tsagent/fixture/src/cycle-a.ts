// One half of a deliberate import cycle: cycle-a -> cycle-b -> cycle-a.

import { formatLabel } from "./cycle-b";

export const PREFIX = "fixture";

export function labelFor(name: string): string {
    return formatLabel(name);
}

// Dead code: unexported and never referenced anywhere.
function neverCalled(): number {
    return 42;
}
