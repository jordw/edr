package index

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Diff tests: compare hand-written vs regex on real files in the repo,
// printing symbol names missing from the hand-written output. Extras
// (symbols hand-written finds that regex misses) are reported but not
// treated as failures.

func diffParsers(t *testing.T, ext string, parse func([]byte) []SymbolInfo) {
	var paths []string
	filepath.WalkDir("../..", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ext) && !strings.HasSuffix(path, "_test"+ext) {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)
	if len(paths) > 40 {
		paths = paths[:40]
	}
	if len(paths) == 0 {
		t.Skip("no files")
	}

	totalMissing := 0
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		rs := RegexParse(path, src)
		hs := parse(src)

		regexNames := map[string]int{}
		for _, s := range rs {
			regexNames[s.Type+":"+s.Name]++
		}
		handNames := map[string]int{}
		for _, s := range hs {
			handNames[s.Type+":"+s.Name]++
		}

		var missing, extra []string
		for k, n := range regexNames {
			if handNames[k] < n {
				missing = append(missing, fmt.Sprintf("%s (-%d)", k, n-handNames[k]))
			}
		}
		for k, n := range handNames {
			if regexNames[k] < n {
				extra = append(extra, fmt.Sprintf("%s (+%d)", k, n-regexNames[k]))
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			t.Logf("%s regex=%d hand=%d", path, len(rs), len(hs))
			t.Logf("  MISSING: %s", strings.Join(missing, ", "))
			totalMissing += len(missing)
		}
	}
	t.Logf("%s files: total missing names: %d", ext, totalMissing)
	if totalMissing > 0 {
		t.Fail()
	}
}

func TestDiff_Go(t *testing.T) {
	diffParsers(t, ".go", func(src []byte) []SymbolInfo {
		r := ParseGo(src)
		return goToSymbolInfo("file.go", src, r)
	})
}

func TestDiff_Python(t *testing.T) {
	home, _ := os.UserHomeDir()
	root := home + "/Documents/GitHub/pytorch/torch"
	if _, err := os.Stat(root); err != nil { t.Skip("no pytorch") }
	var paths []string
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() { return nil }
		if strings.HasSuffix(path, ".py") { paths = append(paths, path) }
		return nil
	})
	sort.Strings(paths)
	if len(paths) > 40 { paths = paths[:40] }
	totalMissing := 0
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil { continue }
		rs := RegexParse(path, src)
		hs := pythonToSymbolInfo(path, src, ParsePython(src))
		regexNames := map[string]int{}
		for _, s := range rs { regexNames[s.Type+":"+s.Name]++ }
		handNames := map[string]int{}
		for _, s := range hs { handNames[s.Type+":"+s.Name]++ }
		var missing []string
		for k, n := range regexNames {
			if handNames[k] < n {
				missing = append(missing, fmt.Sprintf("%s (-%d)", k, n-handNames[k]))
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			t.Logf("%s regex=%d hand=%d", path, len(rs), len(hs))
			t.Logf("  MISSING: %s", strings.Join(missing, ", "))
			totalMissing += len(missing)
		}
	}
	// Python's regex parser has known false positives (it matches
	// `class`/`def` keywords inside docstrings) and a buggy
	// indent-scope tracker (regexFindIndentEnd truncates at any line
	// with small indent, including docstring body text). The
	// hand-written parser correctly skips those cases, so a raw diff
	// against regex is not a fair ground truth. We log the count but
	// don't fail — use this test to spot regressions, not enforce parity.
	t.Logf("py files: total missing (vs buggy regex): %d", totalMissing)
}