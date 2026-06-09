// Discriminated union with a deliberately non-exhaustive switch (used by
// `analyze exhaustiveness` tests: describeShape misses the "triangle" case).

export interface Circle {
    kind: "circle";
    radius: number;
}

export interface Square {
    kind: "square";
    side: number;
}

export interface Triangle {
    kind: "triangle";
    base: number;
    height: number;
}

export type Shape = Circle | Square | Triangle;

// Deliberately missing the "triangle" case.
export function describeShape(shape: Shape): string {
    switch (shape.kind) {
        case "circle":
            return `circle r=${shape.radius}`;
        case "square":
            return `square s=${shape.side}`;
    }
    return "unknown shape";
}

// Exhaustive counterpart.
export function area(shape: Shape): number {
    switch (shape.kind) {
        case "circle":
            return Math.PI * shape.radius ** 2;
        case "square":
            return shape.side ** 2;
        case "triangle":
            return (shape.base * shape.height) / 2;
    }
}
