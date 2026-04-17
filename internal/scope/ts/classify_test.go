package ts

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jordw/edr/internal/scope"
)

// TestDogfood_ClassifyUnresolved buckets every unresolved ref into:
// - ts_builtin: JS/TS global (any, string, Promise, etc.)
// - import_but_unresolved: the file DOES import this name; parser/resolver bug
// - signature_generic: T, K, U, V — likely a generic in signature position
// - convention_external: names like _zod that convention says are external
// - other_miss: everything else (actual parser gaps)
// Gated on EDR_SCOPE_DOGFOOD_DIR; skipped in default CI runs.
func TestDogfood_ClassifyUnresolved(t *testing.T) {
	dir := os.Getenv("EDR_SCOPE_DOGFOOD_DIR")
	if dir == "" {
		t.Skip("EDR_SCOPE_DOGFOOD_DIR not set")
	}

	builtins := map[string]bool{}
	for _, name := range strings.Fields(`
any unknown never void null undefined object Object string String number Number
boolean Boolean bigint BigInt symbol Symbol Array ReadonlyArray Map ReadonlyMap
Set ReadonlySet WeakMap WeakSet Promise RegExp Error TypeError RangeError
SyntaxError ReferenceError Date Math JSON Reflect Proxy console globalThis
NaN Infinity isNaN isFinite parseInt parseFloat Function Generator AsyncGenerator
Iterator AsyncIterator IteratorResult Iterable AsyncIterable IterableIterator
Omit Pick Partial Required Readonly Record Exclude Extract NonNullable
Parameters ReturnType ConstructorParameters InstanceType ThisType Awaited
PromiseLike ArrayLike Uppercase Lowercase Capitalize Uncapitalize keyof typeof
ArrayBuffer Uint8Array Uint16Array Int8Array Uint32Array Int32Array Float32Array
Float64Array process Buffer arguments`) {
		builtins[name] = true
	}

	totals := map[string]int{}
	perBucketNames := map[string]map[string]int{}

	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".ts") || strings.HasSuffix(path, ".d.ts") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		r := Parse(path, src)
		imports := map[string]bool{}
		for _, d := range r.Decls {
			if d.Kind == scope.KindImport {
				imports[d.Name] = true
			}
		}
		for _, ref := range r.Refs {
			if ref.Binding.Kind != scope.BindUnresolved {
				continue
			}
			name := ref.Name
			var bucket string
			switch {
			case builtins[name]:
				bucket = "ts_builtin"
			case imports[name]:
				bucket = "import_but_unresolved"
			case len(name) == 1 && name[0] >= 'A' && name[0] <= 'Z':
				bucket = "signature_generic"
			case strings.HasPrefix(name, "_"):
				bucket = "convention_external"
			default:
				bucket = "other_miss"
			}
			totals[bucket]++
			if perBucketNames[bucket] == nil {
				perBucketNames[bucket] = map[string]int{}
			}
			perBucketNames[bucket][name]++
		}
		return nil
	})

	total := 0
	for _, c := range totals {
		total += c
	}
	t.Logf("total unresolved: %d", total)
	type bc struct {
		bucket string
		count  int
	}
	var bcs []bc
	for b, c := range totals {
		bcs = append(bcs, bc{b, c})
	}
	sort.Slice(bcs, func(i, j int) bool { return bcs[i].count > bcs[j].count })
	for _, x := range bcs {
		pct := 0.0
		if total > 0 {
			pct = 100 * float64(x.count) / float64(total)
		}
		t.Logf("  %-25s %5d  (%.1f%%)", x.bucket, x.count, pct)
	}
	t.Logf("")
	t.Logf("top names per bucket:")
	for _, x := range bcs {
		t.Logf("[%s]", x.bucket)
		names := perBucketNames[x.bucket]
		type nc struct {
			name  string
			count int
		}
		var ncs []nc
		for n, c := range names {
			ncs = append(ncs, nc{n, c})
		}
		sort.Slice(ncs, func(i, j int) bool { return ncs[i].count > ncs[j].count })
		for i, n := range ncs {
			if i >= 10 {
				break
			}
			t.Logf("  %4d  %s", n.count, n.name)
		}
	}
}
