// Uses tsconfig paths aliases to import.
import { compute } from "@/components/counter";
import { compute as computeAlias } from "@components/counter";

export function run(): number {
    return compute(5) + computeAlias(3);
}
