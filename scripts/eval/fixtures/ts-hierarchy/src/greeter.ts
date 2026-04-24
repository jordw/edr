export interface IGreeter {
    greet(): string;
}

export class Hi implements IGreeter {
    greet(): string { return "hi"; }
}

export class Loud extends Hi {
    greet(): string { return "HI!"; }
}
