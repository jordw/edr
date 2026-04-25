package idx

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestExtractTrigrams(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int // minimum expected count
	}{
		{"empty", "", 0},
		{"short", "ab", 0},
		{"exact3", "abc", 1},
		{"simple", "hello", 3}, // hel, ell, llo
		{"repeated", "aaaa", 1}, // aaa (deduplicated)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractTrigrams([]byte(tt.input))
			if len(got) < tt.want {
				t.Errorf("ExtractTrigrams(%q) = %d trigrams, want >= %d", tt.input, len(got), tt.want)
			}
		})
	}
}

func TestExtractTrigramsDeterministic(t *testing.T) {
	data := []byte("func main() { fmt.Println(\"hello world\") }")
	a := ExtractTrigrams(data)
	b := ExtractTrigrams(data)
	if len(a) != len(b) {
		t.Fatalf("non-deterministic: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("trigram %d differs", i)
		}
	}
}

func TestQueryTrigrams(t *testing.T) {
	tris := QueryTrigrams("SearchText")
	if len(tris) != 8 { // Sea, ear, arc, rch, chT, hTe, Tex, ext
		t.Errorf("QueryTrigrams(\"SearchText\") = %d trigrams, want 8", len(tris))
	}

	// Short query
	if tris := QueryTrigrams("ab"); tris != nil {
		t.Errorf("QueryTrigrams(\"ab\") should be nil, got %d", len(tris))
	}
}

func TestFormatRoundTrip(t *testing.T) {
	triMap := map[Trigram][]uint32{
		{'f', 'o', 'o'}: {0, 2, 5},
		{'b', 'a', 'r'}: {1, 3},
		{'b', 'a', 'z'}: {0, 1, 2, 3, 4, 5},
	}
	postings, entries := BuildPostings(triMap)

	d := &Snapshot{
		Header: Header{
			NumFiles:    6,
			NumTrigrams: uint32(len(entries)),
			GitMtime:    1234567890,
		},
		Files: []FileEntry{
			{Path: "a.go", Mtime: 100, Size: 200},
			{Path: "b.go", Mtime: 101, Size: 300},
			{Path: "c.go", Mtime: 102, Size: 400},
			{Path: "dir/d.go", Mtime: 103, Size: 500},
			{Path: "dir/e.go", Mtime: 104, Size: 600},
			{Path: "f.go", Mtime: 105, Size: 700},
		},
		Trigrams: entries,
		Postings: postings,
	}

	data := d.Marshal()
	d2, err := Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if d2.Header.NumFiles != d.Header.NumFiles {
		t.Errorf("NumFiles: got %d, want %d", d2.Header.NumFiles, d.Header.NumFiles)
	}
	if d2.Header.NumTrigrams != d.Header.NumTrigrams {
		t.Errorf("NumTrigrams: got %d, want %d", d2.Header.NumTrigrams, d.Header.NumTrigrams)
	}
	if d2.Header.GitMtime != d.Header.GitMtime {
		t.Errorf("GitMtime: got %d, want %d", d2.Header.GitMtime, d.Header.GitMtime)
	}
	if len(d2.Files) != len(d.Files) {
		t.Fatalf("Files: got %d, want %d", len(d2.Files), len(d.Files))
	}
	for i := range d.Files {
		if d2.Files[i] != d.Files[i] {
			t.Errorf("File[%d]: got %+v, want %+v", i, d2.Files[i], d.Files[i])
		}
	}
	if len(d2.Trigrams) != len(d.Trigrams) {
		t.Fatalf("Trigrams: got %d, want %d", len(d2.Trigrams), len(d.Trigrams))
	}

	// Verify posting lists decode correctly
	for i, te := range d2.Trigrams {
		ids := DecodePosting(d2.Postings, te.Offset, te.Count)
		origIDs := DecodePosting(d.Postings, d.Trigrams[i].Offset, d.Trigrams[i].Count)
		if len(ids) != len(origIDs) {
			t.Errorf("Trigram %v: got %d IDs, want %d", te.Tri, len(ids), len(origIDs))
		}
	}
}

func TestVarintRoundTrip(t *testing.T) {
	values := []uint32{0, 1, 127, 128, 255, 256, 16383, 16384, 1<<21 - 1, 1 << 21}
	for _, v := range values {
		var buf [5]byte
		n := 0
		tmp := v
		for tmp >= 0x80 {
			buf[n] = byte(tmp) | 0x80
			tmp >>= 7
			n++
		}
		buf[n] = byte(tmp)
		n++

		got, sz := readVarint(buf[:n])
		if got != v || sz != n {
			t.Errorf("varint(%d): got (%d, %d), want (%d, %d)", v, got, sz, v, n)
		}
	}
}

func TestIntersect(t *testing.T) {
	a := []uint32{1, 3, 5, 7, 9}
	b := []uint32{2, 3, 5, 8, 9}
	got := intersect(a, b)
	want := []uint32{3, 5, 9}
	if len(got) != len(want) {
		t.Fatalf("intersect: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("intersect[%d]: got %d, want %d", i, got[i], want[i])
		}
	}
}

func TestBuildAndQuery(t *testing.T) {
	dir := t.TempDir()
	edrDir := t.TempDir()

	// Create test files
	writeFile(t, dir, "hello.go", "package main\nfunc main() { fmt.Println(\"hello world\") }")
	writeFile(t, dir, "foo.go", "package main\nfunc foo() { return 42 }")
	writeFile(t, dir, "bar.go", "package main\nfunc bar() { fmt.Println(\"hello\") }")

	// Build full index
	paths := []string{
		filepath.Join(dir, "hello.go"),
		filepath.Join(dir, "foo.go"),
		filepath.Join(dir, "bar.go"),
	}
	d := BuildFull(dir, paths, 0)
	data := d.Marshal()
	os.WriteFile(filepath.Join(edrDir, MainFile), data, 0600)

	// Query for "hello" — should match hello.go and bar.go
	tris := QueryTrigrams("hello")
	candidates, ok := Query(edrDir, tris)
	if !ok {
		t.Fatal("Query returned not-ok")
	}
	sort.Strings(candidates)
	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d: %v", len(candidates), candidates)
	}
	if candidates[0] != "bar.go" || candidates[1] != "hello.go" {
		t.Errorf("expected [bar.go hello.go], got %v", candidates)
	}

	// Query for "return 42" — should match only foo.go
	tris = QueryTrigrams("return 42")
	candidates, ok = Query(edrDir, tris)
	if !ok {
		t.Fatal("Query returned not-ok")
	}
	if len(candidates) != 1 || candidates[0] != "foo.go" {
		t.Errorf("expected [foo.go], got %v", candidates)
	}
}

func TestBuildFullFromWalk(t *testing.T) {
	dir := t.TempDir()
	edrDir := t.TempDir()

	writeFile(t, dir, "main.go", "package main\nfunc main() { fmt.Println(\"trigram test\") }")
	writeFile(t, dir, "lib.go", "package main\nfunc helper() string { return \"data\" }")

	walkFn := func(root string, fn func(string) error) error {
		entries, _ := os.ReadDir(root)
		for _, e := range entries {
			if !e.IsDir() {
				fn(filepath.Join(root, e.Name()))
			}
		}
		return nil
	}

	var progress []int
	err := BuildFullFromWalk(dir, edrDir, walkFn, func(done, total int) {
		progress = append(progress, done)
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(progress) == 0 {
		t.Error("no progress callbacks")
	}

	// Main index should exist
	if _, err := os.Stat(filepath.Join(edrDir, MainFile)); err != nil {
		t.Error("main index not created")
	}

	// Query
	tris := QueryTrigrams("trigram test")
	candidates, ok := Query(edrDir, tris)
	if !ok {
		t.Fatal("Query returned not-ok")
	}
	if len(candidates) != 1 || candidates[0] != "main.go" {
		t.Errorf("expected [main.go], got %v", candidates)
	}
}

func TestGetStatus(t *testing.T) {
	edrDir := t.TempDir()

	// No index
	s := GetStatus("/tmp/fake", edrDir)
	if s.Exists {
		t.Error("should not exist yet")
	}

	// Create index
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "package a\nfunc test() {}")
	d := BuildFull(dir, []string{filepath.Join(dir, "a.go")}, 0)
	os.WriteFile(filepath.Join(edrDir, MainFile), d.Marshal(), 0600)

	s = GetStatus(dir, edrDir)
	if !s.Exists {
		t.Error("should exist")
	}
	if s.Files != 1 {
		t.Errorf("files: got %d, want 1", s.Files)
	}
	if s.SizeBytes == 0 {
		t.Error("size should be > 0")
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, filepath.Dir(name)), 0755)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
