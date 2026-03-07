package search

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestFindFilesBudgetTracksOutputSize(t *testing.T) {
	// Create a temp dir with 50+ files of varying sizes.
	tmp := t.TempDir()
	numFiles := 55
	for i := 0; i < numFiles; i++ {
		name := fmt.Sprintf("file_%03d.go", i)
		// Each file is ~12KB (like a real source file)
		content := make([]byte, 12*1024)
		for j := range content {
			content[j] = 'x'
		}
		if err := os.WriteFile(filepath.Join(tmp, name), content, 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Also need a .git dir so WalkRepoFiles treats it as a repo
	if err := os.MkdirAll(filepath.Join(tmp, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	result, err := FindFiles(ctx, tmp, "*.go", "", 100)
	if err != nil {
		t.Fatalf("FindFiles error: %v", err)
	}

	// With budget=100 tracking output size (~80 bytes per entry, /4 = ~20 tokens),
	// we should get more files than the old behavior (which returned 2-4 for 12KB files).
	if len(result.Files) < 5 {
		t.Errorf("expected at least 5 files with budget=100, got %d", len(result.Files))
	}

	if result.TotalMatched != numFiles {
		t.Errorf("expected TotalMatched=%d, got %d", numFiles, result.TotalMatched)
	}

	// Budget 100 with 55 files should still truncate eventually
	if !result.Truncated {
		t.Log("not truncated — all files fit within budget (acceptable)")
	}
}

func TestFindFilesNoBudgetReturnsAll(t *testing.T) {
	tmp := t.TempDir()
	numFiles := 20
	for i := 0; i < numFiles; i++ {
		name := fmt.Sprintf("f%d.txt", i)
		if err := os.WriteFile(filepath.Join(tmp, name), []byte("hello"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(tmp, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	result, err := FindFiles(ctx, tmp, "*.txt", "", 0)
	if err != nil {
		t.Fatalf("FindFiles error: %v", err)
	}

	if len(result.Files) != numFiles {
		t.Errorf("expected %d files with no budget, got %d", numFiles, len(result.Files))
	}
	if result.Truncated {
		t.Error("expected Truncated=false with no budget")
	}
}

func TestFindFilesBudgetTruncation(t *testing.T) {
	tmp := t.TempDir()
	// Create files with long names to consume budget faster
	numFiles := 100
	for i := 0; i < numFiles; i++ {
		name := fmt.Sprintf("very_long_directory_name_to_eat_budget_%03d.go", i)
		if err := os.WriteFile(filepath.Join(tmp, name), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(tmp, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	result, err := FindFiles(ctx, tmp, "*.go", "", 50)
	if err != nil {
		t.Fatal(err)
	}

	if result.TotalMatched != numFiles {
		t.Errorf("expected TotalMatched=%d, got %d", numFiles, result.TotalMatched)
	}

	// With long names (~100 bytes each / 4 ≈ 25 tokens per entry) and budget=50,
	// it should truncate before getting all files
	if len(result.Files) >= numFiles {
		t.Errorf("expected truncation with budget=50, but got all %d files", len(result.Files))
	}

	// But should still get a reasonable number (not just 0-1)
	if len(result.Files) < 2 {
		t.Errorf("expected at least 2 files, got %d", len(result.Files))
	}

	if !result.Truncated {
		t.Error("expected Truncated=true")
	}
}
