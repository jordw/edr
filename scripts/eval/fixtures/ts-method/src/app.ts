import { Counter } from "./counter";

export function run(): number {
    const c = new Counter();
    return c.value();
}
