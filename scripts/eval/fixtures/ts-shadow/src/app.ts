import { compute } from "./lib";

export function run(): number {
    return compute(5);
}

// Unrelated function with a LOCAL named the same as the import.
// Rename of the imported compute must NOT touch this parameter.
export function wrapper(compute: number): number {
    return compute + 1;
}
