<?php
require_once __DIR__ . "/Counter.php";

function run(): int {
    $c = new Counter();
    return $c->value();
}

echo run() . "
";
