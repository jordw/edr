<?php
require_once __DIR__ . "/Greeter.php";

$h = new Hi();
$l = new Loud();
echo $h->greet() . " " . $l->greet() . "
";
