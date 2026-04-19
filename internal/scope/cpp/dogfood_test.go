package cpp

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jordw/edr/internal/scope"
)

// TestDogfood_RealCPPFiles parses real C++ files under a directory set
// via EDR_SCOPE_CPP_DOGFOOD_DIR and reports resolution stats. Enforces:
// no panics, every unresolved ref has a reason code, no scope=0 refs
// (stack underflow). Mirrors internal/scope/c/dogfood_test.go.
func TestDogfood_RealCPPFiles(t *testing.T) {
	dir := os.Getenv("EDR_SCOPE_CPP_DOGFOOD_DIR")
	if dir == "" {
		t.Skip("EDR_SCOPE_CPP_DOGFOOD_DIR not set")
	}

	var files []string
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			n := info.Name()
			if n == ".git" || n == ".edr" || n == "build" || n == "dist" ||
				n == "out" || n == "third_party" {
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case strings.HasSuffix(path, ".cpp"),
			strings.HasSuffix(path, ".cc"),
			strings.HasSuffix(path, ".cxx"),
			strings.HasSuffix(path, ".hpp"),
			strings.HasSuffix(path, ".hxx"),
			strings.HasSuffix(path, ".hh"):
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)

	var totalRefs, resolved, unresolvedLocal, unresolvedExternal, scopeZero int
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					t.Errorf("PANIC on %s: %v", f, rec)
				}
			}()
			r := Parse(f, src)
			localNames := map[string]bool{}
			for _, d := range r.Decls {
				localNames[d.Name] = true
			}
			for _, ref := range r.Refs {
				totalRefs++
				if ref.Scope == 0 {
					scopeZero++
				}
				if ref.Binding.Kind == scope.BindResolved {
					resolved++
					continue
				}
				if ref.Binding.Reason == "" {
					t.Errorf("%s: unresolved ref %q missing reason", f, ref.Name)
				}
				if localNames[ref.Name] {
					unresolvedLocal++
				} else {
					unresolvedExternal++
				}
			}
		}()
	}

	t.Logf("files parsed:       %d", len(files))
	t.Logf("total refs:         %d", totalRefs)
	if totalRefs > 0 {
		t.Logf("resolved:           %d (%.1f%%)",
			resolved, 100*float64(resolved)/float64(totalRefs))
		t.Logf("unresolved local:   %d (%.1f%%) (name IS declared — scope miss)",
			unresolvedLocal, 100*float64(unresolvedLocal)/float64(totalRefs))
		t.Logf("unresolved external:%d (%.1f%%) (name NOT in file — include/template/etc)",
			unresolvedExternal, 100*float64(unresolvedExternal)/float64(totalRefs))
		resolvable := resolved + unresolvedLocal
		if resolvable > 0 {
			t.Logf("in-file resolution: %.1f%% of refs whose target is in-file",
				100*float64(resolved)/float64(resolvable))
		}
	}
	if scopeZero > 0 {
		t.Errorf("%d refs have scope=0 (stack underflow)", scopeZero)
	}
}
