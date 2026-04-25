<?php
interface IGreeter {
    public function greet(): string;
}

class Hi implements IGreeter {
    public function greet(): string { return "hi"; }
}

class Loud extends Hi {
    public function greet(): string { return "HI!"; }
}
