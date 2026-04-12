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

    // PHP 8 attribute on method — parser skips #[...] and records getName normally
    #[Route('/api')]
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

// PHP 8.2 readonly class — "readonly" is a modifier; parser produces "class"
readonly class Config
{
    public function __construct(public string $name) {}
}
`)
	r := ParsePHP(src)
	for i, s := range r.Symbols {
		t.Logf("[%d] %-9s %-20s L%d-%d parent=%d", i, s.Type, s.Name, s.StartLine, s.EndLine, s.Parent)
	}
	for i, imp := range r.Imports {
		t.Logf("imp[%d] %s L%d", i, imp.Path, imp.Line)
	}

	// Strict ordered assertion — every symbol is verified.
	want := []struct{ typ, name string }{
		{"class", "Widget"},
		{"function", "__construct"},
		{"function", "getName"},    // preceded by #[Route('/api')] — attribute skipped
		{"function", "create"},
		{"function", "doWork"},
		{"class", "Drawable"},
		{"function", "draw"},
		{"class", "Cacheable"},
		{"function", "cache"},
		{"class", "Status"},
		{"function", "label"},
		{"function", "freeFunction"},
		{"class", "Config"},        // readonly class
		{"function", "__construct"},
	}

	if len(r.Symbols) != len(want) {
		t.Errorf("got %d symbols, want %d", len(r.Symbols), len(want))
		for i, s := range r.Symbols {
			t.Logf("  [%d] %s %q", i, s.Type, s.Name)
		}
	}
	for i, w := range want {
		if i >= len(r.Symbols) {
			t.Errorf("symbol %d missing: want %s %q", i, w.typ, w.name)
			continue
		}
		if r.Symbols[i].Type != w.typ || r.Symbols[i].Name != w.name {
			t.Errorf("symbol %d: got %s %q, want %s %q",
				i, r.Symbols[i].Type, r.Symbols[i].Name, w.typ, w.name)
		}
	}
	if len(r.Symbols) > len(want) {
		for i := len(want); i < len(r.Symbols); i++ {
			t.Errorf("unexpected extra symbol %d: %s %q", i, r.Symbols[i].Type, r.Symbols[i].Name)
		}
	}

	for _, s := range r.Symbols {
		if s.Name == "Fake" || s.Name == "AlsoFake" {
			t.Errorf("spurious symbol from comment/string: %+v", s)
		}
	}

	wantImps := []string{
		"Illuminate\\Database\\Eloquent\\Model",
		"Illuminate\\Support\\Collection",
	}
	if len(r.Imports) != len(wantImps) {
		t.Errorf("got %d imports, want %d", len(r.Imports), len(wantImps))
		for _, imp := range r.Imports {
			t.Logf("  import %q", imp.Path)
		}
	}
	for i, w := range wantImps {
		if i >= len(r.Imports) {
			break
		}
		if r.Imports[i].Path != w {
			t.Errorf("import %d: got %q want %q", i, r.Imports[i].Path, w)
		}
	}
}
