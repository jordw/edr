import { compute as computeA } from "./lib/a";
import { compute as computeB } from "./lib/b";

export function run(): number {
    return computeA(5) + computeB(3);
}
