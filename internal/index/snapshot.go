package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

const snapshotVersion = 1

// RepoSnapshot is the persisted repo snapshot used to detect whether the
// index was built against the current filesystem state.
type RepoSnapshot struct {
	Version   int    `json:"version"`
	FileCount int    `json:"file_count"`
	RootHash  string `json:"root_hash"`
}

type snapshotLeaf struct {
	relPath string
	hash    [32]byte
}

// ComputeRepoSnapshot builds a Merkle-style root over index-relevant files.
// It includes supported source files plus the root .gitignore file.
func ComputeRepoSnapshot(ctx context.Context, root string) (RepoSnapshot, error) {
	root, err := NormalizeRoot(root)
	if err != nil {
		return RepoSnapshot{}, err
	}

	gitignore := LoadGitIgnore(root)
	var leaves []snapshotLeaf

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if shouldIgnoreDir(d.Name(), path, root, gitignore) {
				return filepath.SkipDir
			}
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if gitignore != nil && gitignore.IsIgnored(rel, false) {
			return nil
		}
		if !shouldSnapshotPath(path) {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Size() > 1<<20 && filepath.Base(path) != ".gitignore" {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		leaves = append(leaves, snapshotLeaf{
			relPath: rel,
			hash:    snapshotLeafHash(rel, data),
		})
		return nil
	})
	if err != nil {
		return RepoSnapshot{}, err
	}

	sort.Slice(leaves, func(i, j int) bool {
		return leaves[i].relPath < leaves[j].relPath
	})

	rootHash := merkleRoot(leaves)
	return RepoSnapshot{
		Version:   snapshotVersion,
		FileCount: len(leaves),
		RootHash:  hex.EncodeToString(rootHash[:]),
	}, nil
}

func shouldSnapshotPath(path string) bool {
	return filepath.Base(path) == ".gitignore" || GetLangConfig(path) != nil
}

func snapshotLeafHash(rel string, data []byte) [32]byte {
	h := sha256.New()
	h.Write([]byte(rel))
	h.Write([]byte{0})
	h.Write(data)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func merkleRoot(leaves []snapshotLeaf) [32]byte {
	if len(leaves) == 0 {
		return sha256.Sum256(nil)
	}

	level := make([][32]byte, len(leaves))
	for i, leaf := range leaves {
		level[i] = leaf.hash
	}

	for len(level) > 1 {
		next := make([][32]byte, 0, (len(level)+1)/2)
		for i := 0; i < len(level); i += 2 {
			left := level[i]
			right := left
			if i+1 < len(level) {
				right = level[i+1]
			}

			var combined [64]byte
			copy(combined[:32], left[:])
			copy(combined[32:], right[:])
			next = append(next, sha256.Sum256(combined[:]))
		}
		level = next
	}

	return level[0]
}

func snapshotPath(root string) string {
	return filepath.Join(root, ".edr", "repo.snapshot.json")
}

// ReadIndexedSnapshot loads the last snapshot persisted after indexing.
func ReadIndexedSnapshot(root string) (RepoSnapshot, bool, error) {
	data, err := os.ReadFile(snapshotPath(root))
	if err != nil {
		if os.IsNotExist(err) {
			return RepoSnapshot{}, false, nil
		}
		return RepoSnapshot{}, false, err
	}

	var snap RepoSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return RepoSnapshot{}, false, err
	}
	return snap, true, nil
}

// WriteIndexedSnapshot persists the snapshot used for future stale checks.
func WriteIndexedSnapshot(root string, snap RepoSnapshot) error {
	path := snapshotPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}

	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// RemoveIndexedSnapshot invalidates the persisted snapshot so freshness checks
// fall back to the slower path until the next full index.
func RemoveIndexedSnapshot(root string) error {
	err := os.Remove(snapshotPath(root))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func updateIndexedSnapshot(ctx context.Context, root string) error {
	snap, err := ComputeRepoSnapshot(ctx, root)
	if err != nil {
		return err
	}
	return WriteIndexedSnapshot(root, snap)
}

// gitignoreMeta stores hashes and mtimes of .gitignore files for stale detection.
// Persisted separately from the files table to avoid inflating file counts.
type gitignoreMeta struct {
	Files map[string]gitignoreEntry `json:"files"` // path → entry
}

type gitignoreEntry struct {
	Hash  string `json:"hash"`
	Mtime int64  `json:"mtime"` // UnixNano
}

func gitignoreMetaPath(root string) string {
	return filepath.Join(root, ".edr", "gitignore.json")
}

// persistGitignoreMeta walks the repo for .gitignore files and stores their
// hashes and mtimes in a separate metadata file.
func persistGitignoreMeta(root string) {
	meta := gitignoreMeta{Files: make(map[string]gitignoreEntry)}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == ".edr" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Base(path) != ".gitignore" {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		h := sha256.Sum256(src)
		meta.Files[rel] = gitignoreEntry{
			Hash:  hex.EncodeToString(h[:8]),
			Mtime: info.ModTime().UnixNano(),
		}
		return nil
	})

	data, err := json.Marshal(meta)
	if err != nil {
		return
	}
	p := gitignoreMetaPath(root)
	_ = os.MkdirAll(filepath.Dir(p), 0700)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err == nil {
		_ = os.Rename(tmp, p)
	}
}

// loadGitignoreMeta reads the persisted .gitignore metadata.
func loadGitignoreMeta(root string) (gitignoreMeta, bool) {
	data, err := os.ReadFile(gitignoreMetaPath(root))
	if err != nil {
		return gitignoreMeta{}, false
	}
	var meta gitignoreMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return gitignoreMeta{}, false
	}
	return meta, true
}

// checkGitignoreStale returns true if any .gitignore file has changed
// since the metadata was persisted.
func checkGitignoreStale(root string) bool {
	meta, ok := loadGitignoreMeta(root)
	if !ok {
		// No metadata → check if .gitignore exists (first index)
		_, err := os.Stat(filepath.Join(root, ".gitignore"))
		return err == nil // stale if gitignore exists but no metadata
	}

	// Check each known .gitignore
	for rel, entry := range meta.Files {
		path := filepath.Join(root, rel)
		info, err := os.Stat(path)
		if err != nil {
			return true // deleted
		}
		if info.ModTime().UnixNano() != entry.Mtime {
			// Mtime changed — verify by content hash
			src, err := os.ReadFile(path)
			if err != nil {
				return true
			}
			h := sha256.Sum256(src)
			if hex.EncodeToString(h[:8]) != entry.Hash {
				return true
			}
		}
	}

	// Check for new .gitignore files
	foundNew := false
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == ".edr" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Base(path) == ".gitignore" {
			rel, _ := filepath.Rel(root, path)
			if _, known := meta.Files[rel]; !known {
				foundNew = true
				return filepath.SkipAll
			}
		}
		return nil
	})

	return foundNew
}
