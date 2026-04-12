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

func BenchmarkPython_Handwritten(b *testing.B) {
	home, _ := os.UserHomeDir()
	files := collectFiles(home+"/Documents/GitHub/pytorch/torch", ".py", 1000)
	if len(files) == 0 { b.Skip("no files") }
	var total int
	for _, f := range files { total += len(f) }
	b.SetBytes(int64(total / len(files)))
	b.ResetTimer()
	var syms int
	for i := 0; i < b.N; i++ {
		r := ParsePython(files[i%len(files)])
		syms += len(r.Symbols)
	}
	b.ReportMetric(float64(syms)/float64(b.N), "syms/op")
}

func BenchmarkGo_Handwritten(b *testing.B) {
	files := collectFiles("../..", ".go", 500)
	if len(files) == 0 { b.Skip("no files") }
	var total int
	for _, f := range files { total += len(f) }
	b.SetBytes(int64(total / len(files)))
	b.ResetTimer()
	var syms int
	for i := 0; i < b.N; i++ {
		r := ParseGo(files[i%len(files)])
		syms += len(r.Symbols)
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

