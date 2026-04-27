package dispatch_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jordw/edr/internal/dispatch"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

func tsFixture(t *testing.T, files map[string]string) (index.SymbolStore, string) {
	t.Helper()
	tmp := t.TempDir()
	// tsconfig.json with no special paths so the namespace resolver
	// uses the default file-suffix resolution.
	if _, ok := files["tsconfig.json"]; !ok {
		files["tsconfig.json"] = `{"compilerOptions":{"baseUrl":"."}}`
	}
	for rel, body := range files {
		full := filepath.Join(tmp, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	t.Cleanup(func() { db.Close() })
	return db, tmp
}

// TestRename_TS_NamedImport: the bread-and-butter ESM path. A named
// import from the renamed file's module gets rewritten; a same-named
// function on an unrelated import is left alone.
func TestRename_TS_NamedImport(t *testing.T) {
	db, dir := tsFixture(t, map[string]string{
		"src/lib.ts":   "export function compute(x: number): number { return x * 2; }\n",
		"src/other.ts": "export function compute(x: number): number { return x + 1; }\n",
		"src/app.ts": `import { compute } from "./lib";
import { compute as otherCompute } from "./other";

export function run(): number {
    return compute(5) + otherCompute(7);
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{filepath.Join(dir, "src/lib.ts") + ":compute"},
		map[string]any{"new_name": "calculate", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	app, _ := os.ReadFile(filepath.Join(dir, "src/app.ts"))
	got := string(app)
	// The namespace-renamed import line must rewrite.
	if !strings.Contains(got, `{ calculate }`) && !strings.Contains(got, `{calculate}`) {
		t.Errorf("named import for ./lib should be rewritten to calculate; got:\n%s", got)
	}
	// The unrelated `compute` import (from ./other) must NOT.
	if !strings.Contains(got, `compute as otherCompute`) {
		t.Errorf("unrelated `compute as otherCompute` import must NOT be renamed; got:\n%s", got)
	}
	// Body usage of the aliased import (`otherCompute(7)`) must be preserved.
	if !strings.Contains(got, "otherCompute(7)") {
		t.Errorf("unrelated otherCompute call must be preserved; got:\n%s", got)
	}
}

// TestRename_TS_DefaultExport covers a quirk the audit flagged: a
// default-exported function whose ALIAS at the import site differs
// from its declared name. The rename target is the declared name in
// the source file; whether the resolver rewrites the alias is
// language-specific. Pin the current behavior so any change is
// intentional.
func TestRename_TS_DefaultExport(t *testing.T) {
	db, dir := tsFixture(t, map[string]string{
		"src/lib.ts": `export default function compute(x: number): number {
    return x * 2;
}
`,
		"src/app.ts": `import compute from "./lib";

export function run(): number {
    return compute(5);
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{filepath.Join(dir, "src/lib.ts") + ":compute"},
		map[string]any{"new_name": "calculate", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	lib, _ := os.ReadFile(filepath.Join(dir, "src/lib.ts"))
	if !strings.Contains(string(lib), "function calculate") {
		t.Errorf("declaration should be renamed; got:\n%s", lib)
	}
}

// TestRename_JS_CommonJSRequire pins the registry's intentional
// fall-through for CJS require destructuring. The TS namespace
// resolver doesn't model `const { X } = require(...)` so an empty
// result there must hand off to the generic ref-filtering path
// (which still rewrites the call site by name).
func TestRename_JS_CommonJSRequire(t *testing.T) {
	db, dir := tsFixture(t, map[string]string{
		"lib.js": `function compute(x) { return x * 2; }
module.exports = { compute };
`,
		"app.js": `const { compute } = require("./lib");
function run() { return compute(5); }
module.exports = { run };
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{filepath.Join(dir, "lib.js") + ":compute"},
		map[string]any{"new_name": "calculate", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	lib, _ := os.ReadFile(filepath.Join(dir, "lib.js"))
	if !strings.Contains(string(lib), "function calculate") {
		t.Errorf("lib declaration should be renamed; got:\n%s", lib)
	}
}
