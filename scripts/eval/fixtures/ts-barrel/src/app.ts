// Imports through the barrel, not directly from ./lib.
import { compute, computeAlias } from "./index";

export function run(): number {
    return compute(5) + computeAlias(3);
}
