package python

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jordw/edr/internal/scope"
)

// TestDogfood_RealPythonFiles parses real Python files under a directory
// set via EDR_SCOPE_PY_DOGFOOD_DIR. Reports resolution stats. Enforces:
// no panics, every unresolved ref has a reason code, no scope=0 refs.
func TestDogfood_RealPythonFiles(t *testing.T) {
	dir := os.Getenv("EDR_SCOPE_PY_DOGFOOD_DIR")
	if dir == "" {
		t.Skip("EDR_SCOPE_PY_DOGFOOD_DIR not set")
	}

	var files []string
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			n := info.Name()
			if n == ".git" || n == ".edr" || n == "__pycache__" || n == ".venv" || n == "node_modules" || n == "build" || n == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".py") && !strings.HasSuffix(path, "_test.py") {
			files = append(files, path)
		}
		return nil
	})

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
				if ref.Binding.Kind == scope.BindProbable {
					// Property access — don't count as "unresolved" since that's expected.
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
		t.Logf("resolved + probable:%d (%.1f%%)",
			resolved, 100*float64(resolved)/float64(totalRefs))
		t.Logf("unresolved local:   %d (%.1f%%)",
			unresolvedLocal, 100*float64(unresolvedLocal)/float64(totalRefs))
		t.Logf("unresolved external:%d (%.1f%%)",
			unresolvedExternal, 100*float64(unresolvedExternal)/float64(totalRefs))
	}
	if scopeZero > 0 {
		t.Errorf("%d refs have scope=0 (stack underflow)", scopeZero)
	}
}
