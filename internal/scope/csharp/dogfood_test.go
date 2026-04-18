package csharp

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jordw/edr/internal/scope"
)

// TestDogfood_RealCSharpFiles parses real C# files from a directory set
// via EDR_SCOPE_CSHARP_DOGFOOD_DIR. Reports per-file stats and enforces
// invariants: parse must not panic, every Ref must have a binding kind
// and (when unresolved) a reason code. Skipped if env var is unset.
func TestDogfood_RealCSharpFiles(t *testing.T) {
	dir := os.Getenv("EDR_SCOPE_CSHARP_DOGFOOD_DIR")
	if dir == "" {
		t.Skip("EDR_SCOPE_CSHARP_DOGFOOD_DIR not set")
	}

	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "bin", "obj", "packages", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".cs") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	sort.Strings(files)

	type fileStats struct {
		path       string
		bytes      int
		scopes     int
		decls      int
		refs       int
		resolved   int
		unresolved int
	}
	var stats []fileStats
	reasonCounts := map[string]int{}

	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Errorf("read %s: %v", f, err)
			continue
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("PANIC parsing %s: %v", f, r)
				}
			}()
			r := Parse(f, src)
			s := fileStats{path: f, bytes: len(src), scopes: len(r.Scopes), decls: len(r.Decls), refs: len(r.Refs)}
			for _, ref := range r.Refs {
				switch ref.Binding.Kind {
				case scope.BindResolved:
					s.resolved++
				case scope.BindUnresolved:
					s.unresolved++
					if ref.Binding.Reason == "" {
						t.Errorf("%s: unresolved ref %q missing reason at span [%d:%d]",
							f, ref.Name, ref.Span.StartByte, ref.Span.EndByte)
					}
					reasonCounts[ref.Binding.Reason]++
				}
			}
			for _, ref := range r.Refs {
				if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl == 0 {
					t.Errorf("%s: Resolved ref %q has zero DeclID", f, ref.Name)
				}
			}
			stats = append(stats, s)
		}()
	}

	totalDecls, totalRefs, totalRes, totalUnres := 0, 0, 0, 0
	for _, s := range stats {
		totalDecls += s.decls
		totalRefs += s.refs
		totalRes += s.resolved
		totalUnres += s.unresolved
	}
	t.Logf("=== csharp dogfood summary ===")
	t.Logf("files parsed:       %d", len(stats))
	t.Logf("total decls:        %d", totalDecls)
	t.Logf("total refs:         %d", totalRefs)
	if totalRefs > 0 {
		t.Logf("resolved:           %d (%.1f%%)", totalRes, 100*float64(totalRes)/float64(totalRefs))
		t.Logf("unresolved:         %d (%.1f%%)", totalUnres, 100*float64(totalUnres)/float64(totalRefs))
	}
	t.Logf("unresolved reasons:")
	type rc struct {
		reason string
		count  int
	}
	var rcs []rc
	for r, c := range reasonCounts {
		rcs = append(rcs, rc{r, c})
	}
	sort.Slice(rcs, func(i, j int) bool { return rcs[i].count > rcs[j].count })
	for _, x := range rcs {
		t.Logf("  %-25s %d", x.reason, x.count)
	}

	sort.Slice(stats, func(i, j int) bool { return stats[i].decls > stats[j].decls })
	t.Logf("top 5 files by decl count:")
	for i, s := range stats {
		if i >= 5 {
			break
		}
		t.Logf("  %4d decls %4d refs %5.1f%% resolved  %s",
			s.decls, s.refs,
			pct(s.resolved, s.refs),
			shortPath(s.path, dir))
	}
}

func pct(num, den int) float64 {
	if den == 0 {
		return 0
	}
	return 100 * float64(num) / float64(den)
}

func shortPath(p, dir string) string {
	if rel, err := filepath.Rel(dir, p); err == nil {
		return rel
	}
	return p
}

var _ = fmt.Sprintf
