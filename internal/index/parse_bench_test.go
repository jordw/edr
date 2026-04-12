package index

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Real-repo benchmarks: hand-written parsers vs current RegexParse.
// Set EDR_RUBY_REPO / EDR_TS_REPO to override defaults.

func collectFiles(root, ext string, limit int) [][]byte {
	var out [][]byte
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ext) {
			return nil
		}
		if ext == ".ts" && strings.HasSuffix(path, ".d.ts") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		out = append(out, b)
		if limit > 0 && len(out) >= limit {
			return filepath.SkipAll
		}
		return nil
	})
	return out
}

func rubyRepo() string {
	if v := os.Getenv("EDR_RUBY_REPO"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Documents/GitHub/rails")
}

func tsRepo() string {
	if v := os.Getenv("EDR_TS_REPO"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Documents/GitHub/vscode/src")
}

func BenchmarkRuby_Regex(b *testing.B) {
	files := collectFiles(rubyRepo(), ".rb", 2000)
	if len(files) == 0 {
		b.Skip("no ruby files")
	}
	var total int
	for _, f := range files {
		total += len(f)
	}
	b.SetBytes(int64(total / len(files)))
	b.ResetTimer()
	var syms int
	for i := 0; i < b.N; i++ {
		f := files[i%len(files)]
		s := RegexParse("file.rb", f)
		syms += len(s)
	}
	b.ReportMetric(float64(syms)/float64(b.N), "syms/op")
}

func BenchmarkRuby_Handwritten(b *testing.B) {
	files := collectFiles(rubyRepo(), ".rb", 2000)
	if len(files) == 0 {
		b.Skip("no ruby files")
	}
	var total int
	for _, f := range files {
		total += len(f)
	}
	b.SetBytes(int64(total / len(files)))
	b.ResetTimer()
	var syms int
	for i := 0; i < b.N; i++ {
		f := files[i%len(files)]
		r := ParseRuby(f)
		syms += len(r.Symbols)
	}
	b.ReportMetric(float64(syms)/float64(b.N), "syms/op")
}

func BenchmarkTS_Regex(b *testing.B) {
	files := collectFiles(tsRepo(), ".ts", 2000)
	if len(files) == 0 {
		b.Skip("no ts files")
	}
	var total int
	for _, f := range files {
		total += len(f)
	}
	b.SetBytes(int64(total / len(files)))
	b.ResetTimer()
	var syms int
	for i := 0; i < b.N; i++ {
		f := files[i%len(files)]
		s := RegexParse("file.ts", f)
		syms += len(s)
	}
	b.ReportMetric(float64(syms)/float64(b.N), "syms/op")
}

func BenchmarkTS_Handwritten(b *testing.B) {
	files := collectFiles(tsRepo(), ".ts", 2000)
	if len(files) == 0 {
		b.Skip("no ts files")
	}
	var total int
	for _, f := range files {
		total += len(f)
	}
	b.SetBytes(int64(total / len(files)))
	b.ResetTimer()
	var syms int
	for i := 0; i < b.N; i++ {
		f := files[i%len(files)]
		r := ParseTS(f)
		syms += len(r.Symbols)
	}
	b.ReportMetric(float64(syms)/float64(b.N), "syms/op")
}

// Correctness sanity check: parse N files with both, report per-file symbol count diffs.
func TestCompareParsers_Ruby(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	files := collectFiles(rubyRepo(), ".rb", 500)
	if len(files) == 0 {
		t.Skip("no ruby files")
	}
	var regexTotal, handTotal int
	var filesWithDiff int
	for _, f := range files {
		rs := RegexParse("file.rb", f)
		hs := ParseRuby(f)
		regexTotal += len(rs)
		handTotal += len(hs.Symbols)
		if len(rs) != len(hs.Symbols) {
			filesWithDiff++
		}
	}
	t.Logf("ruby: %d files, regex=%d syms, hand=%d syms, %d files differ",
		len(files), regexTotal, handTotal, filesWithDiff)
}

func TestCompareParsers_TS(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	files := collectFiles(tsRepo(), ".ts", 500)
	if len(files) == 0 {
		t.Skip("no ts files")
	}
	var regexTotal, handTotal int
	var filesWithDiff int
	for _, f := range files {
		rs := RegexParse("file.ts", f)
		hs := ParseTS(f)
		regexTotal += len(rs)
		handTotal += len(hs.Symbols)
		if len(rs) != len(hs.Symbols) {
			filesWithDiff++
		}
	}
	t.Logf("ts: %d files, regex=%d syms, hand=%d syms, %d files differ",
		len(files), regexTotal, handTotal, filesWithDiff)
}