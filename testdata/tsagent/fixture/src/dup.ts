// Deliberate structural clone pair (used by `analyze duplicates`): the two
// functions are identical after identifier normalization (a type-2 clone).

export function sumPositiveSquares(values: number[]): number {
    let total = 0;
    for (const value of values) {
        if (value > 0) {
            total += value * value;
        }
    }
    return total;
}

export function sumPositiveWeights(items: number[]): number {
    let acc = 0;
    for (const item of items) {
        if (item > 0) {
            acc += item * item;
        }
    }
    return acc;
}
