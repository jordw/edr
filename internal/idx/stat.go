package idx

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
)

// Changes holds the result of comparing indexed file metadata against the
// filesystem. Modified files have a different mtime or size than the index.
// Deleted files no longer exist on disk. New files exist in directories
// whose mtime changed but are not in the index.
type Changes struct {
	Modified []string // relative paths — mtime or size differs
	Deleted  []string // relative paths — file no longer exists
	New      []string // relative paths — not in index
}

// Empty returns true if no changes were detected.
func (c *Changes) Empty() bool {
	return len(c.Modified) == 0 && len(c.Deleted) == 0 && len(c.New) == 0
}

// StatChanges loads the file table from the index and parallel-stats every
// file to find modifications, deletions, and new files. Costs ~66ms on a
// 93K-file repo (Linux kernel). Returns nil if no index exists.
func StatChanges(root, edrDir string) *Changes {
	// Mmap the index and walk the file table in-place — avoids reading
	// 5MB into heap and allocating 93K FileEntry structs.
	f, err := os.Open(filepath.Join(edrDir, MainFile))
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() < int64(v2HeaderSize) {
		return nil
	}
	data, err := syscall.Mmap(int(f.Fd()), 0, int(fi.Size()),
		syscall.PROT_READ, syscall.MAP_PRIVATE)
	if err != nil {
		return nil
	}
	defer syscall.Munmap(data)

	h, err := ReadHeaderBytes(data)
	if err != nil || h.NumFiles == 0 {
		return nil
	}

	// Parse file entries from mmap into lightweight path+mtime+size slices.
	type fileRef struct {
		path  string
		mtime int64
		size  int64
	}
	numFiles := int(h.NumFiles)
	refs := make([]fileRef, 0, numFiles)
	ftData := data[h.FileTableOff:h.PostingOff]
	pos := 0
	for i := 0; i < numFiles; i++ {
		if pos+2 > len(ftData) {
			break
		}
		pathLen := int(binary.LittleEndian.Uint16(ftData[pos:]))
		pos += 2
		if pos+pathLen+16 > len(ftData) {
			break
		}
		p := string(ftData[pos : pos+pathLen])
		pos += pathLen
		mtime := int64(binary.LittleEndian.Uint64(ftData[pos:]))
		pos += 8
		size := int64(binary.LittleEndian.Uint64(ftData[pos:]))
		pos += 8
		refs = append(refs, fileRef{path: p, mtime: mtime, size: size})
	}

	if len(refs) == 0 {
		return nil
	}

	type statResult struct {
		rel     string
		deleted bool
		changed bool
	}

	// Parallel stat all indexed files.
	workers := runtime.GOMAXPROCS(0)
	if workers < 4 {
		workers = 4
	}
	ch := make(chan int, 256)
	results := make([]statResult, len(refs))
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ri := range ch {
				ref := &refs[ri]
				abs := filepath.Join(root, ref.path)
				info, err := os.Lstat(abs)
				if err != nil {
					results[ri] = statResult{rel: ref.path, deleted: true}
				} else if info.ModTime().UnixNano() != ref.mtime || info.Size() != ref.size {
					results[ri] = statResult{rel: ref.path, changed: true}
				}
			}
		}()
	}
	for i := range refs {
		ch <- i
	}
	close(ch)
	wg.Wait()

	// Build directory mtime map and indexed set for new-file detection.
	indexedDirs := make(map[string]int64, 4096)
	indexedSet := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		indexedSet[ref.path] = struct{}{}
		dir := filepath.Dir(ref.path)
		if ref.mtime > indexedDirs[dir] {
			indexedDirs[dir] = ref.mtime
		}
	}

	c := &Changes{}
	for _, r := range results {
		switch {
		case r.deleted:
			c.Deleted = append(c.Deleted, r.rel)
		case r.changed:
			c.Modified = append(c.Modified, r.rel)
		}
	}

	// Scan directories for new files. A directory with a changed mtime
	// has had files created or deleted. We check every indexed directory.
	// When we encounter a NEW subdirectory (not in indexedDirs), recurse
	// into it to find files created in newly-made directory trees
	// (e.g. edr edit --mkdir creating tools/testing/foo.c).
	for dir, maxMtime := range indexedDirs {
		info, err := os.Stat(filepath.Join(root, dir))
		if err != nil {
			continue
		}
		if info.ModTime().UnixNano() <= maxMtime {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			continue
		}
		for _, e := range entries {
			rel := filepath.Join(dir, e.Name())
			if e.IsDir() {
				// Recurse into directories that aren't in our indexed set.
				if _, known := indexedDirs[rel]; !known {
					walkNewDir(root, rel, indexedSet, &c.New)
				}
				continue
			}
			if _, indexed := indexedSet[rel]; !indexed {
				c.New = append(c.New, rel)
			}
		}
	}

	return c
}

// walkNewDir recursively walks a directory that wasn't in the index,
// adding all non-indexed files to the new list. Skips ignored paths.
func walkNewDir(root, dir string, indexed map[string]struct{}, out *[]string) {
	entries, err := os.ReadDir(filepath.Join(root, dir))
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		// Skip hidden and common ignored directories.
		if strings.HasPrefix(name, ".") {
			continue
		}
		rel := filepath.Join(dir, name)
		if e.IsDir() {
			if name == "node_modules" || name == "vendor" || name == "target" || name == "build" {
				continue
			}
			walkNewDir(root, rel, indexed, out)
			continue
		}
		if _, alreadyIndexed := indexed[rel]; !alreadyIndexed {
			*out = append(*out, rel)
		}
	}
}

// isBinary returns true when the first 512 bytes of data contain a NUL.
// Used to skip binary files during indexing and patching.
func isBinary(data []byte) bool {
	check := data
	if len(check) > 512 {
		check = check[:512]
	}
	for _, b := range check {
		if b == 0 {
			return true
		}
	}
	return false
}
