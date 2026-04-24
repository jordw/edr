import { IGreeter, Hi, Loud } from "./greeter";

export function run(): string {
    const g: IGreeter = new Hi();
    const l: Loud = new Loud();
    return g.greet() + " " + l.greet();
}
