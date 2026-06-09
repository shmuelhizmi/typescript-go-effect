// Entry point: ties the fixture together and instantiates the generic
// Container with two different type arguments.

import { labelFor } from "./cycle-a";
import { Container, Dog, Puppy } from "./models";
import { describeShape, type Shape } from "./shapes";

export { area } from "./shapes";

export function main(): string {
    const dog = new Dog("Rex");
    const puppy = new Puppy("Bolt");
    const names = new Container<string>("hello");
    const counts = new Container<number>(3);
    const shape: Shape = { kind: "circle", radius: counts.get() };
    return [
        dog.speak(),
        puppy.speak(),
        names.get(),
        describeShape(shape),
        labelFor("demo"),
    ].join(" | ");
}
