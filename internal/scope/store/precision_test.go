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

// TestPrecision_ImportGraphContribution measures what the import-graph
// resolver actually contributes to refs-to precision. For each Decl that
// has at least one "import_export" or "include_resolution" ref bound to
// it (i.e., a decl the resolver touched), reports the per-decl
// breakdown: direct_scope vs import_export/include_resolution vs
// unresolved-that-would-shadow-this-name.
//
// Run with EDR_SCOPE_DOGFOOD_DIR=<repo> and optionally
// EDR_SCOPE_PRECISION_TOP=<N> (default 20) to limit top-decl output.
func TestPrecision_ImportGraphContribution(t *testing.T) {
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
					name == "vendor" || name == "__pycache__" {
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
	_, err = Build(abs, edrDir, walk)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Gather: decl name/file/kind by DeclID, and per-DeclID ref
	// counts broken down by reason. Also count unresolved refs by
	// name repo-wide so we know how many additional refs COULD
	// potentially bind to this decl but weren't reached by the
	// resolver (shadow budget).
	type declInfo struct {
		id   scope.DeclID
		name string
		file string
		kind string
		refsByReason map[string]int
	}
	byDecl := map[scope.DeclID]*declInfo{}
	unresolvedByName := map[string]int{}

	for rel := range idx.header.Records {
		r := idx.ResultFor(abs, rel)
		if r == nil {
			continue
		}
		for i := range r.Decls {
			d := &r.Decls[i]
			if _, ok := byDecl[d.ID]; !ok {
				byDecl[d.ID] = &declInfo{
					id: d.ID, name: d.Name, file: d.File, kind: string(d.Kind),
					refsByReason: map[string]int{},
				}
			}
		}
		for _, ref := range r.Refs {
			if ref.Binding.Kind == scope.BindUnresolved {
				unresolvedByName[ref.Name]++
				continue
			}
			if ref.Binding.Decl == 0 {
				continue
			}
			d := byDecl[ref.Binding.Decl]
			if d == nil {
				continue
			}
			d.refsByReason[ref.Binding.Reason]++
		}
	}

	// Filter to decls touched by the resolver.
	type row struct {
		name         string
		file         string
		kind         string
		direct       int
		importExport int
		includeRes   int
		thisDot      int
		other        int
		unresolved   int // repo-wide unresolved with same name
	}
	var rows []row
	for _, d := range byDecl {
		rr := row{name: d.name, file: d.file, kind: d.kind}
		for reason, n := range d.refsByReason {
			switch reason {
			case "direct_scope":
				rr.direct += n
			case "import_export":
				rr.importExport += n
			case "include_resolution":
				rr.includeRes += n
			case "this_dot_field", "self_dot_field":
				rr.thisDot += n
			default:
				rr.other += n
			}
		}
		if rr.importExport == 0 && rr.includeRes == 0 {
			continue
		}
		rr.unresolved = unresolvedByName[d.name]
		rows = append(rows, rr)
	}

	topN := 20
	if v := os.Getenv("EDR_SCOPE_PRECISION_TOP"); v != "" {
		if n := atoiDefault(v, 20); n > 0 {
			topN = n
		}
	}

	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		return (a.importExport + a.includeRes) > (b.importExport + b.includeRes)
	})

	t.Logf("=== Top %d decls by import-graph contribution ===", topN)
	t.Logf("%-6s %-30s %-10s %6s %6s %6s %6s %6s %8s",
		"rank", "name", "kind", "direct", "impEx", "inclR", "thisD", "other", "unresvd")
	limit := topN
	if limit > len(rows) {
		limit = len(rows)
	}
	for i, r := range rows[:limit] {
		t.Logf("%-6d %-30s %-10s %6d %6d %6d %6d %6d %8d",
			i+1, truncate(r.name, 30), truncate(r.kind, 10),
			r.direct, r.importExport, r.includeRes, r.thisDot, r.other, r.unresolved)
	}

	// Aggregate impact.
	var totalImpEx, totalInclRes, totalDirect int
	for _, r := range rows {
		totalImpEx += r.importExport
		totalInclRes += r.includeRes
		totalDirect += r.direct
	}
	t.Logf("")
	t.Logf("decls touched by resolver: %d", len(rows))
	t.Logf("total refs rewritten: import_export=%d, include_resolution=%d, combined=%d",
		totalImpEx, totalInclRes, totalImpEx+totalInclRes)
	t.Logf("total direct_scope across same decls: %d", totalDirect)
	if totalDirect > 0 {
		t.Logf("resolver added %.1f%% on top of direct same-file resolution (for touched decls)",
			100*float64(totalImpEx+totalInclRes)/float64(totalDirect))
	}
}

func atoiDefault(s string, d int) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return d
		}
		n = n*10 + int(c-'0')
	}
	if n == 0 {
		return d
	}
	return n
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
