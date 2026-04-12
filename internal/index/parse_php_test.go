package index

import "testing"

func TestParsePHP_Fixture(t *testing.T) {
	src := []byte(`<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Support\Collection as Items;

// class Fake
/* interface AlsoFake */

class Widget extends Model implements Serializable
{
    private string $name;

    public function __construct(string $name)
    {
        $this->name = $name;
    }

    public function getName(): string
    {
        $s = "class Fake {}";
        return $this->name;
    }

    public static function create(string $name): self
    {
        return new self($name);
    }

    abstract public function doWork(): void;
}

interface Drawable
{
    public function draw(): void;
}

trait Cacheable
{
    public function cache(): void {}
}

enum Status: string
{
    case Active = 'active';
    case Inactive = 'inactive';

    public function label(): string
    {
        return $this->value;
    }
}

function freeFunction(int $x): int
{
    return $x + 1;
}
`)
	r := ParsePHP(src)
	for i, s := range r.Symbols {
		t.Logf("[%d] %-9s %-20s L%d-%d parent=%d", i, s.Type, s.Name, s.StartLine, s.EndLine, s.Parent)
	}
	for i, imp := range r.Imports {
		t.Logf("imp[%d] %s L%d", i, imp.Path, imp.Line)
	}

	found := map[string]bool{}
	for _, s := range r.Symbols {
		found[s.Type+":"+s.Name] = true
	}
	mustHave := []string{
		"class:Widget",
		"class:Drawable",
		"class:Cacheable",
		"class:Status",
	}
	for _, want := range mustHave {
		if !found[want] {
			t.Errorf("missing symbol: %s", want)
		}
	}
	if found["class:Fake"] || found["class:AlsoFake"] {
		t.Error("spurious symbol from comment/string")
	}
	if len(r.Imports) < 2 {
		t.Errorf("got %d imports, want at least 2", len(r.Imports))
	}
}
