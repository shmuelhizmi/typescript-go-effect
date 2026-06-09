// Class/interface hierarchy plus a generic container that is instantiated
// with two different type arguments in index.ts.

export interface Animal {
    readonly name: string;
    speak(): string;
}

export class Dog implements Animal {
    constructor(readonly name: string) {}

    speak(): string {
        return `${this.name} says woof`;
    }
}

export class Puppy extends Dog {
    override speak(): string {
        return `${this.name} yips`;
    }
}

export class Container<T> {
    constructor(private value: T) {}

    get(): T {
        return this.value;
    }

    set(value: T): void {
        this.value = value;
    }
}
