package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// DefaultIgnore contains directories to skip during indexing.
var DefaultIgnore = []string{
	".git", ".edr", "node_modules", "vendor", "__pycache__",
	".venv", "venv", "target", "build", "dist", ".next",
	".idea", ".vscode", "bin", "obj",
}

// IndexRepo indexes all supported files in the repository.
func IndexRepo(ctx context.Context, db *DB) (int, int, error) {
	root := db.Root()
	var filesIndexed, symbolsFound int

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip errors
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Skip ignored directories
		if d.IsDir() {
			name := d.Name()
			for _, ign := range DefaultIgnore {
				if name == ign {
					return filepath.SkipDir
				}
			}
			return nil
		}

		// Skip non-supported files
		lang := GetLangConfig(path)
		if lang == nil {
			return nil
		}

		// Skip large files (>1MB)
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Size() > 1<<20 {
			return nil
		}

		// Check if file needs re-indexing
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		hash := fileHash(src)
		storedHash, _ := db.GetFileHash(ctx, path)
		if storedHash == hash {
			return nil // already indexed, skip
		}

		// Parse and index
		symbols, err := ParseSource(path, src, lang)
		if err != nil {
			return nil // skip parse errors
		}

		// Update database
		if err := db.UpsertFile(ctx, path, hash, info.ModTime().Unix()); err != nil {
			return nil
		}
		if err := db.ClearSymbols(ctx, path); err != nil {
			return nil
		}

		for _, sym := range symbols {
			if err := db.InsertSymbol(ctx, sym); err != nil {
				return nil
			}
			symbolsFound++
		}

		filesIndexed++
		return nil
	})

	return filesIndexed, symbolsFound, err
}

// IndexFile re-indexes a single file, updating the DB with fresh symbols.
func IndexFile(ctx context.Context, db *DB, path string) error {
	lang := GetLangConfig(path)
	if lang == nil {
		return nil // unsupported language, nothing to index
	}

	src, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("indexfile: read: %w", err)
	}

	hash := fileHash(src)
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("indexfile: stat: %w", err)
	}

	symbols, err := ParseSource(path, src, lang)
	if err != nil {
		return fmt.Errorf("indexfile: parse: %w", err)
	}

	if err := db.UpsertFile(ctx, path, hash, info.ModTime().Unix()); err != nil {
		return err
	}
	if err := db.ClearSymbols(ctx, path); err != nil {
		return err
	}
	for _, sym := range symbols {
		if err := db.InsertSymbol(ctx, sym); err != nil {
			return err
		}
	}
	return nil
}

// WalkRepoFiles calls fn for every non-ignored, non-binary file in the repo.
// It respects the same ignore list as IndexRepo and skips files > 1MB.
func WalkRepoFiles(root string, fn func(path string) error) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			for _, ign := range DefaultIgnore {
				if name == ign {
					return filepath.SkipDir
				}
			}
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > 1<<20 {
			return nil
		}
		return fn(path)
	})
}

func fileHash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:4]) // first 8 hex chars
}

// RepoMap generates a concise map of the repository structure.
func RepoMap(ctx context.Context, db *DB) (string, error) {
	symbols, err := db.AllSymbols(ctx)
	if err != nil {
		return "", err
	}

	// Group by file
	byFile := make(map[string][]SymbolInfo)
	var fileOrder []string
	for _, s := range symbols {
		if _, seen := byFile[s.File]; !seen {
			fileOrder = append(fileOrder, s.File)
		}
		byFile[s.File] = append(byFile[s.File], s)
	}

	var b strings.Builder
	root := db.Root()
	for _, file := range fileOrder {
		rel, _ := filepath.Rel(root, file)
		if rel == "" {
			rel = file
		}
		fmt.Fprintf(&b, "\n%s\n", rel)
		for _, sym := range byFile[file] {
			fmt.Fprintf(&b, "  %s %s [%d-%d]\n", sym.Type, sym.Name, sym.StartLine, sym.EndLine)
		}
	}

	return b.String(), nil
}
