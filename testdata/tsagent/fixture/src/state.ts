// Transition functions over the Shape discriminated union (used by
// `diagram state`): first parameter and return type both relate to Shape.

import type { Circle, Shape, Square } from "./shapes";

// Any shape can be squared up: every state transitions to "square".
export function squareUp(shape: Shape): Square {
    switch (shape.kind) {
        case "circle":
            return { kind: "square", side: shape.radius };
        case "square":
            return shape;
        case "triangle":
            return { kind: "square", side: shape.base };
    }
}

// Only squares roll: "square" transitions to "circle".
export function roll(shape: Square): Circle {
    return { kind: "circle", radius: shape.side / 2 };
}
