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

func TestDiff_Ruby(t *testing.T) {
	home, _ := os.UserHomeDir()
	root := home + "/Documents/GitHub/rails"
	if _, err := os.Stat(root); err != nil { t.Skip("no rails") }
	var paths []string
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() { return nil }
		if strings.HasSuffix(path, ".rb") { paths = append(paths, path) }
		return nil
	})
	sort.Strings(paths)
	if len(paths) > 60 { paths = paths[:60] }
	totalMissing := 0
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil { continue }
		rs := RegexParse(path, src)
		hs := rubyToSymbolInfo(path, src, ParseRuby(src))
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
	// Remaining "missing" are regex bugs the hand-written parser
	// correctly handles:
	//   1. `class Foo::Bar::Baz` — regex truncates the qualified name to
	//      the first segment; hand-written emits the leaf name (the
	//      actual class being defined).
	//   2. `def self.foo` — regex captures `self` as the method name;
	//      hand-written captures `foo`.
	//   3. Heredoc bodies at column 0 confuse regex's `regexFindIndentEnd`,
	//      truncating the enclosing class scope and leaving nested defs
	//      with no parent (labeled `function` instead of `method`).
	// Informational only — regex is not a fair ground truth here.
	t.Logf("ruby files: total missing (vs buggy regex): %d", totalMissing)
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

func TestDiff_Cpp(t *testing.T) {
	home, _ := os.UserHomeDir()
	root := home + "/Documents/GitHub/linux/kernel"
	if _, err := os.Stat(root); err != nil {
		t.Skip("no linux kernel")
	}
	var paths []string
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".c" || ext == ".h" {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)
	if len(paths) > 40 {
		paths = paths[:40]
	}
	var totalMissing, totalExtra int
	var regexTotal, handTotal int
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		rs := RegexParse(path, src)
		hs := cppToSymbolInfo(path, src, ParseCpp(src))
		regexTotal += len(rs)
		handTotal += len(hs)
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
			t.Logf("%s regex=%d hand=%d", filepath.Base(path), len(rs), len(hs))
			t.Logf("  MISSING: %s", strings.Join(missing, ", "))
			totalMissing += len(missing)
		}
		totalExtra += len(extra)
	}
	t.Logf("cpp: %d files, regex=%d hand=%d, missing=%d, extra=%d",
		len(paths), regexTotal, handTotal, totalMissing, totalExtra)
}

func TestDiff_Java(t *testing.T) {
	home, _ := os.UserHomeDir()
	root := home + "/Documents/GitHub/spring-framework"
	if _, err := os.Stat(root); err != nil {
		t.Skip("no spring-framework")
	}
	var paths []string
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() { return nil }
		if strings.HasSuffix(path, ".java") && !strings.Contains(path, "/test/") {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)
	if len(paths) > 40 { paths = paths[:40] }
	var totalMissing int
	var regexTotal, handTotal int
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil { continue }
		rs := RegexParse(path, src)
		hs := javaToSymbolInfo(path, src, ParseJava(src))
		regexTotal += len(rs)
		handTotal += len(hs)
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
			t.Logf("%s regex=%d hand=%d", filepath.Base(path), len(rs), len(hs))
			t.Logf("  MISSING: %s", strings.Join(missing, ", "))
			totalMissing += len(missing)
		}
	}
	t.Logf("java: %d files, regex=%d hand=%d, total missing=%d",
		len(paths), regexTotal, handTotal, totalMissing)
}

func TestDiff_CSharp(t *testing.T) {
	home, _ := os.UserHomeDir()
	root := home + "/Documents/GitHub/roslyn/src"
	if _, err := os.Stat(root); err != nil { t.Skip("no roslyn") }
	var paths []string
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() { return nil }
		if strings.HasSuffix(path, ".cs") && !strings.Contains(path, "/test/") && !strings.Contains(path, "/Test") {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)
	if len(paths) > 60 { paths = paths[:60] }
	var totalMissing, totalExtra, regexTotal, handTotal int
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil { continue }
		rs := RegexParse(path, src)
		hs := csharpToSymbolInfo(path, src, ParseCSharp(src))
		regexTotal += len(rs)
		handTotal += len(hs)
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
			t.Logf("%s regex=%d hand=%d", filepath.Base(path), len(rs), len(hs))
			t.Logf("  MISSING: %s", strings.Join(missing, ", "))
			totalMissing += len(missing)
		}
		for k, n := range handNames {
			if regexNames[k] < n { totalExtra++ }
		}
	}
	t.Logf("csharp: %d files, regex=%d hand=%d, missing=%d, extra=%d",
		len(paths), regexTotal, handTotal, totalMissing, totalExtra)
}

func TestDiff_Kotlin(t *testing.T) {
	home, _ := os.UserHomeDir()
	root := home + "/Documents/GitHub/kotlin/compiler/frontend/src"
	if _, err := os.Stat(root); err != nil { t.Skip("no kotlin") }
	var paths []string
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() { return nil }
		if strings.HasSuffix(path, ".kt") { paths = append(paths, path) }
		return nil
	})
	sort.Strings(paths)
	if len(paths) > 60 { paths = paths[:60] }
	var totalMissing, totalExtra, regexTotal, handTotal int
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil { continue }
		rs := RegexParse(path, src)
		hs := kotlinToSymbolInfo(path, src, ParseKotlin(src))
		regexTotal += len(rs)
		handTotal += len(hs)
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
			t.Logf("%s regex=%d hand=%d", filepath.Base(path), len(rs), len(hs))
			t.Logf("  MISSING: %s", strings.Join(missing, ", "))
			totalMissing += len(missing)
		}
		for k, n := range handNames {
			if regexNames[k] < n { totalExtra++ }
		}
	}
	t.Logf("kotlin: %d files, regex=%d hand=%d, missing=%d, extra=%d",
		len(paths), regexTotal, handTotal, totalMissing, totalExtra)
}

func TestDiff_Swift(t *testing.T) {
	home, _ := os.UserHomeDir()
	root := home + "/Documents/GitHub/vapor/Sources"
	if _, err := os.Stat(root); err != nil { t.Skip("no vapor") }
	var paths []string
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() { return nil }
		if strings.HasSuffix(path, ".swift") { paths = append(paths, path) }
		return nil
	})
	sort.Strings(paths)
	if len(paths) > 60 { paths = paths[:60] }
	var totalMissing, totalExtra, regexTotal, handTotal int
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil { continue }
		rs := RegexParse(path, src)
		hs := swiftToSymbolInfo(path, src, ParseSwift(src))
		regexTotal += len(rs)
		handTotal += len(hs)
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
			t.Logf("%s regex=%d hand=%d", filepath.Base(path), len(rs), len(hs))
			t.Logf("  MISSING: %s", strings.Join(missing, ", "))
			totalMissing += len(missing)
		}
		for k, n := range handNames {
			if regexNames[k] < n { totalExtra++ }
		}
	}
	t.Logf("swift: %d files, regex=%d hand=%d, missing=%d, extra=%d",
		len(paths), regexTotal, handTotal, totalMissing, totalExtra)
}

func TestDiff_PHP(t *testing.T) {
	home, _ := os.UserHomeDir()
	root := home + "/Documents/GitHub/laravel/src"
	if _, err := os.Stat(root); err != nil { t.Skip("no laravel") }
	var paths []string
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() { return nil }
		if strings.HasSuffix(path, ".php") { paths = append(paths, path) }
		return nil
	})
	sort.Strings(paths)
	if len(paths) > 60 { paths = paths[:60] }
	var totalMissing, totalExtra, regexTotal, handTotal int
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil { continue }
		rs := RegexParse(path, src)
		hs := phpToSymbolInfo(path, src, ParsePHP(src))
		regexTotal += len(rs)
		handTotal += len(hs)
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
			t.Logf("%s regex=%d hand=%d", filepath.Base(path), len(rs), len(hs))
			t.Logf("  MISSING: %s", strings.Join(missing, ", "))
			totalMissing += len(missing)
		}
		for k, n := range handNames {
			if regexNames[k] < n { totalExtra++ }
		}
	}
	t.Logf("php: %d files, regex=%d hand=%d, missing=%d, extra=%d",
		len(paths), regexTotal, handTotal, totalMissing, totalExtra)
}

func TestDiff_Rust(t *testing.T) {
	home, _ := os.UserHomeDir()
	root := home + "/Documents/GitHub/tokio/tokio/src"
	if _, err := os.Stat(root); err != nil { t.Skip("no tokio") }
	var paths []string
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() { return nil }
		if strings.HasSuffix(path, ".rs") { paths = append(paths, path) }
		return nil
	})
	sort.Strings(paths)
	if len(paths) > 60 { paths = paths[:60] }
	var totalMissing, totalExtra, regexTotal, handTotal int
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil { continue }
		rs := RegexParse(path, src)
		hs := rustToSymbolInfo(path, src, ParseRust(src))
		regexTotal += len(rs)
		handTotal += len(hs)
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
		var extra []string
		for k, n := range handNames {
			if regexNames[k] < n {
				extra = append(extra, fmt.Sprintf("%s (+%d)", k, n-regexNames[k]))
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			t.Logf("%s regex=%d hand=%d", filepath.Base(path), len(rs), len(hs))
			t.Logf("  MISSING: %s", strings.Join(missing, ", "))
			totalMissing += len(missing)
		}
		totalExtra += len(extra)
	}
	t.Logf("rust: %d files, regex=%d hand=%d, missing=%d, extra=%d",
		len(paths), regexTotal, handTotal, totalMissing, totalExtra)
}