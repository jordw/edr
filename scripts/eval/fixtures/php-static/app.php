<?php
require_once __DIR__ . "/Lib.php";

function run(): int {
    return Lib::compute(5);
}

echo run() . "
";
