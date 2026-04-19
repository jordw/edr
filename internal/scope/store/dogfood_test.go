package store

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jordw/edr/internal/scope"
)

// TestDogfood_ImportGraph_TS runs the full Build pipeline (parse +
// reconcile + import-graph rewrite) against a real TypeScript repo and
// reports aggregate ref resolution counts. Skipped unless
// EDR_SCOPE_DOGFOOD_DIR is set (also accepts EDR_SCOPE_TS_DOGFOOD_DIR
// for parity with earlier docs).
//
// This test exists to measure the delta from the Phase 1 import graph.
// Per-file dogfood tests (internal/scope/ts/dogfood_test.go) only see
// local-scope resolution; they cannot observe refs rewritten from
// "direct_scope → local Import" to "import_export → exported decl in
// another file".
func TestDogfood_ImportGraph_TS(t *testing.T) {
	dir := os.Getenv("EDR_SCOPE_DOGFOOD_DIR")
	if dir == "" {
		dir = os.Getenv("EDR_SCOPE_TS_DOGFOOD_DIR")
	}
	if dir == "" {
		t.Skip("EDR_SCOPE_DOGFOOD_DIR (or EDR_SCOPE_TS_DOGFOOD_DIR) not set")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}

	// walkFn: same filter as Build but tightened to TS-only so the test
	// runs in reasonable time on big monorepos. We still run Build over
	// the full TS tree; non-TS files would just be filtered by Build's
	// ext switch anyway, but walking them costs I/O.
	walk := func(root string, fn func(string) error) error {
		return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				name := d.Name()
				if name == "node_modules" || name == ".git" || name == "build" ||
					name == "dist" || name == "compiled" {
					return filepath.SkipDir
				}
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			switch ext {
			case ".ts", ".tsx", ".mts", ".cts":
				return fn(path)
			}
			return nil
		})
	}

	edrDir := t.TempDir()
	n, err := Build(abs, edrDir, walk)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Logf("Build: indexed %d files", n)

	idx, err := Load(edrDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Aggregate stats across all TS records.
	var totalRefs, resolved, unresolved int
	reasonCounts := map[string]int{}
	importExportCount := 0
	for rel := range idx.header.Records {
		if !isTSLike(rel) {
			continue
		}
		r := idx.ResultFor(abs, rel)
		if r == nil {
			continue
		}
		for _, ref := range r.Refs {
			totalRefs++
			switch ref.Binding.Kind {
			case scope.BindResolved:
				resolved++
				if ref.Binding.Reason == "import_export" {
					importExportCount++
				}
				reasonCounts["resolved:"+ref.Binding.Reason]++
			case scope.BindUnresolved:
				unresolved++
				reasonCounts["unresolved:"+ref.Binding.Reason]++
			}
		}
	}

	t.Logf("=== Phase 1 import-graph dogfood ===")
	t.Logf("total refs:         %d", totalRefs)
	if totalRefs > 0 {
		t.Logf("resolved:           %d (%.1f%%)", resolved, 100*float64(resolved)/float64(totalRefs))
		t.Logf("  of which import_export: %d (%.1f%% of all refs)",
			importExportCount, 100*float64(importExportCount)/float64(totalRefs))
		t.Logf("unresolved:         %d (%.1f%%)", unresolved, 100*float64(unresolved)/float64(totalRefs))
	}
	type rc struct {
		r string
		n int
	}
	var rcs []rc
	for r, c := range reasonCounts {
		rcs = append(rcs, rc{r, c})
	}
	sort.Slice(rcs, func(i, j int) bool { return rcs[i].n > rcs[j].n })
	t.Logf("reason breakdown:")
	for _, x := range rcs {
		t.Logf("  %-40s %d", x.r, x.n)
	}
}

// TestDogfood_ImportGraph_AllLanguages runs Build over every file with a
// supported extension in EDR_SCOPE_DOGFOOD_DIR and reports per-language
// ref-resolution stats plus the cross-file rewrite counts produced by
// each resolveImports<Lang>. Skipped unless the env var is set.
//
// Primary signal: per-language "import_export %" — the fraction of all
// refs the language's resolver successfully rewrote from a local Import
// decl to an exported Decl in another file. Secondary signal: BindResolved
// as a whole, which rolls up direct_scope + import_export + builtin +
// language-specific reasons.
func TestDogfood_ImportGraph_AllLanguages(t *testing.T) {
	dir := os.Getenv("EDR_SCOPE_DOGFOOD_DIR")
	if dir == "" {
		t.Skip("EDR_SCOPE_DOGFOOD_DIR not set")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}

	walk := func(root string, fn func(string) error) error {
		return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				name := d.Name()
				if name == "node_modules" || name == ".git" || name == "build" ||
					name == "dist" || name == "compiled" || name == "target" ||
					name == "vendor" || name == "__pycache__" || name == ".venv" ||
					name == "venv" {
					return filepath.SkipDir
				}
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			switch ext {
			case ".ts", ".tsx", ".js", ".jsx", ".mts", ".cts",
				".go", ".py", ".pyi",
				".java", ".rs", ".rb",
				".c", ".h",
				".cpp", ".cc", ".cxx", ".hpp", ".hxx", ".hh",
				".cs", ".swift", ".kt", ".kts", ".php":
				return fn(path)
			}
			return nil
		})
	}

	edrDir := t.TempDir()
	n, err := Build(abs, edrDir, walk)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Logf("Build: indexed %d files from %s", n, abs)

	idx, err := Load(edrDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// extToLang classifies a file extension into a coarse language
	// bucket for reporting. Kept inside the test because reconcile.go's
	// languageGroup is narrower (only the langs that support cross-file
	// decl merging).
	extToLang := func(ext string) string {
		switch ext {
		case ".ts", ".tsx", ".mts", ".cts":
			return "ts"
		case ".js", ".jsx":
			return "js"
		case ".go":
			return "go"
		case ".py", ".pyi":
			return "python"
		case ".java":
			return "java"
		case ".rs":
			return "rust"
		case ".rb":
			return "ruby"
		case ".c", ".h":
			return "c"
		case ".cpp", ".cc", ".cxx", ".hpp", ".hxx", ".hh":
			return "cpp"
		case ".cs":
			return "csharp"
		case ".swift":
			return "swift"
		case ".kt", ".kts":
			return "kotlin"
		case ".php":
			return "php"
		}
		return "other"
	}

	type langStats struct {
		files        int
		totalRefs    int
		resolved     int
		probable     int
		ambiguous    int
		unresolved   int
		importExport int
	}
	perLang := map[string]*langStats{}
	reasonsPerLang := map[string]map[string]int{}

	for rel := range idx.header.Records {
		lang := extToLang(strings.ToLower(filepath.Ext(rel)))
		r := idx.ResultFor(abs, rel)
		if r == nil {
			continue
		}
		ls := perLang[lang]
		if ls == nil {
			ls = &langStats{}
			perLang[lang] = ls
			reasonsPerLang[lang] = map[string]int{}
		}
		ls.files++
		for _, ref := range r.Refs {
			ls.totalRefs++
			switch ref.Binding.Kind {
			case scope.BindResolved:
				ls.resolved++
				// Count any cross-file Phase-1 rewrite: TS-style
				// import_export + C/C++ include_resolution +
				// qualified_member (C++ namespace-qualified access).
				// Same meaning ("resolver moved this ref from
				// unresolved/local to the actual definition"), just
				// different reason strings chosen per language.
				switch ref.Binding.Reason {
				case "import_export", "include_resolution", "qualified_member":
					ls.importExport++
				}
				reasonsPerLang[lang]["resolved:"+ref.Binding.Reason]++
			case scope.BindProbable:
				ls.probable++
				reasonsPerLang[lang]["probable:"+ref.Binding.Reason]++
			case scope.BindAmbiguous:
				ls.ambiguous++
				reasonsPerLang[lang]["ambiguous:"+ref.Binding.Reason]++
			case scope.BindUnresolved:
				ls.unresolved++
				reasonsPerLang[lang]["unresolved:"+ref.Binding.Reason]++
			}
		}
	}

	langs := make([]string, 0, len(perLang))
	for l := range perLang {
		langs = append(langs, l)
	}
	sort.Strings(langs)

	pct := func(n, total int) float64 {
		if total == 0 {
			return 0
		}
		return 100 * float64(n) / float64(total)
	}

	t.Logf("=== Phase 1 import-graph — per-language ===")
	t.Logf("%-8s %8s %10s %8s %8s %8s %8s %12s",
		"lang", "files", "refs", "resolv%", "probab%", "ambig%", "unres%", "import_ex%")
	for _, l := range langs {
		s := perLang[l]
		t.Logf("%-8s %8d %10d %7.1f%% %7.1f%% %7.1f%% %7.1f%% %11.1f%%",
			l, s.files, s.totalRefs,
			pct(s.resolved, s.totalRefs),
			pct(s.probable, s.totalRefs),
			pct(s.ambiguous, s.totalRefs),
			pct(s.unresolved, s.totalRefs),
			pct(s.importExport, s.totalRefs))
	}

	t.Logf("=== Top reasons per language ===")
	for _, l := range langs {
		rsm := reasonsPerLang[l]
		if len(rsm) == 0 {
			continue
		}
		type rc struct {
			r string
			n int
		}
		var rcs []rc
		for r, c := range rsm {
			rcs = append(rcs, rc{r, c})
		}
		sort.Slice(rcs, func(i, j int) bool { return rcs[i].n > rcs[j].n })
		limit := 8
		if limit > len(rcs) {
			limit = len(rcs)
		}
		t.Logf("  %s:", l)
		for _, x := range rcs[:limit] {
			t.Logf("    %-40s %d", x.r, x.n)
		}
	}
}
