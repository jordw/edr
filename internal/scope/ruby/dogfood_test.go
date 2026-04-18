package ruby

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jordw/edr/internal/scope"
)

// TestDogfood_RealRubyFiles parses real Ruby files under a directory set
// via EDR_SCOPE_RUBY_DOGFOOD_DIR. Reports resolution stats. Enforces:
// no panics, no scope=0 refs, every unresolved ref has a reason code.
func TestDogfood_RealRubyFiles(t *testing.T) {
	dir := os.Getenv("EDR_SCOPE_RUBY_DOGFOOD_DIR")
	if dir == "" {
		t.Skip("EDR_SCOPE_RUBY_DOGFOOD_DIR not set")
	}

	var files []string
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			n := info.Name()
			if n == ".git" || n == ".edr" || n == "vendor" || n == "node_modules" || n == "tmp" || n == "log" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".rb") && !strings.HasSuffix(path, "_test.rb") {
			files = append(files, path)
		}
		return nil
	})

	var totalRefs, resolved, probable, unresolved, scopeZero int
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
			for _, ref := range r.Refs {
				totalRefs++
				if ref.Scope == 0 {
					scopeZero++
				}
				switch ref.Binding.Kind {
				case scope.BindResolved:
					resolved++
				case scope.BindProbable:
					probable++
				default:
					unresolved++
					if ref.Binding.Reason == "" {
						t.Errorf("%s: unresolved ref %q missing reason", f, ref.Name)
					}
				}
			}
		}()
	}

	t.Logf("files parsed:    %d", len(files))
	t.Logf("total refs:      %d", totalRefs)
	if totalRefs > 0 {
		t.Logf("resolved:        %d (%.1f%%)", resolved, 100*float64(resolved)/float64(totalRefs))
		t.Logf("probable:        %d (%.1f%%)", probable, 100*float64(probable)/float64(totalRefs))
		t.Logf("unresolved:      %d (%.1f%%)", unresolved, 100*float64(unresolved)/float64(totalRefs))
	}
	if scopeZero > 0 {
		t.Errorf("%d refs have scope=0 (stack underflow)", scopeZero)
	}
}
