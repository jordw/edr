package idx

import (
	"hash/fnv"
	"sort"
	"strings"
	"sync"
)

// NameHash computes a stable hash for symbol name lookup.
func NameHash(name string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(strings.ToLower(name)))
	return h.Sum64()
}

// BuildNamePostings builds name-keyed posting lists from symbol entries.
// Returns the posting data and sorted posting table entries.
func BuildNamePostings(symbols []SymbolEntry) ([]byte, []NamePostEntry) {
	// Group symbol IDs by name hash
	byHash := make(map[uint64][]uint32)
	for i, s := range symbols {
		h := NameHash(s.Name)
		byHash[h] = append(byHash[h], uint32(i))
	}

	// Sort hashes for binary search
	hashes := make([]uint64, 0, len(byHash))
	for h := range byHash {
		hashes = append(hashes, h)
	}
	sort.Slice(hashes, func(i, j int) bool { return hashes[i] < hashes[j] })

	// Build posting data (varint-encoded symbol IDs)
	var postData []byte
	entries := make([]NamePostEntry, len(hashes))
	for i, h := range hashes {
		ids := byHash[h]
		sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
		offset := uint64(len(postData))
		// Delta-varint encode
		prev := uint32(0)
		for _, id := range ids {
			delta := id - prev
			postData = appendVarint(postData, delta)
			prev = id
		}
		entries[i] = NamePostEntry{
			NameHash: h,
			Count:    uint32(len(ids)),
			Offset:   offset,
		}
	}
	return postData, entries
}

func appendVarint(buf []byte, v uint32) []byte {
	for v >= 0x80 {
		buf = append(buf, byte(v)|0x80)
		v >>= 7
	}
	buf = append(buf, byte(v))
	return buf
}

// QuerySymbolsByName looks up symbols by exact name in a loaded index.
// SymbolWithID pairs a symbol entry with its index position in the symbol table.
type SymbolWithID struct {
	SymbolEntry
	IndexID uint32
}

// QuerySymbolsWithIDs is like QuerySymbolsByName but also returns each symbol's
// index position for popularity score lookups.
func QuerySymbolsWithIDs(d *Snapshot, name string) []SymbolWithID {
	if len(d.NamePosts) == 0 {
		return nil
	}
	h := NameHash(name)
	lo, hi := 0, len(d.NamePosts)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		if d.NamePosts[mid].NameHash == h {
			np := d.NamePosts[mid]
			ids := DecodePosting(d.NamePostings, np.Offset, np.Count)
			nameLower := strings.ToLower(name)
			var results []SymbolWithID
			for _, id := range ids {
				if int(id) < len(d.Symbols) {
					s := d.Symbols[id]
					if strings.ToLower(s.Name) == nameLower {
						results = append(results, SymbolWithID{SymbolEntry: s, IndexID: id})
					}
				}
			}
			return results
		}
		if d.NamePosts[mid].NameHash < h {
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return nil
}

func QuerySymbolsByName(d *Snapshot, name string) []SymbolEntry {
	if len(d.NamePosts) == 0 {
		return nil
	}
	h := NameHash(name)
	// Binary search the name posting table
	lo, hi := 0, len(d.NamePosts)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		if d.NamePosts[mid].NameHash == h {
			// Decode posting list
			np := d.NamePosts[mid]
			ids := DecodePosting(d.NamePostings, np.Offset, np.Count)
			nameLower := strings.ToLower(name)
			var results []SymbolEntry
			for _, id := range ids {
				if int(id) < len(d.Symbols) {
					s := d.Symbols[id]
					// Verify exact match (hash collision guard)
					if strings.ToLower(s.Name) == nameLower {
						results = append(results, s)
					}
				}
			}
			return results
		}
		if d.NamePosts[mid].NameHash < h {
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return nil
}

// QuerySymbolsByHash returns symbols matching a pre-computed name hash.
// Unlike QuerySymbolsByName, this cannot verify exact name match (hash collision guard),
// so results may include hash collisions. Callers should verify names if needed.
func QuerySymbolsByHash(d *Snapshot, h uint64) []SymbolEntry {
	if len(d.NamePosts) == 0 {
		return nil
	}
	lo, hi := 0, len(d.NamePosts)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		if d.NamePosts[mid].NameHash == h {
			np := d.NamePosts[mid]
			ids := DecodePosting(d.NamePostings, np.Offset, np.Count)
			var results []SymbolEntry
			for _, id := range ids {
				if int(id) < len(d.Symbols) {
					results = append(results, d.Symbols[id])
				}
			}
			return results
		}
		if d.NamePosts[mid].NameHash < h {
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return nil
}

// AllIndexedSymbols returns all symbols from a loaded index.
func AllIndexedSymbols(d *Snapshot) []SymbolEntry {
	return d.Symbols
}

// HasSymbolIndex returns true if the index has v3 symbol data.
func HasSymbolIndex(edrDir string) bool {
	h, err := ReadHeader(edrDir)
	if err != nil {
		return false
	}
	return h.Version >= 3 && h.NumSymbols > 0
}

// cachedSymbolIndex holds the lightweight symbol-only index.
// This avoids loading the full trigram index (which can be 200MB+)
// when only symbol lookups are needed.
var (
	symIdxMu    sync.Mutex
	symIdxDir   string
	symIdxFiles []FileEntry
	symIdxData  *Snapshot // only Symbols, NamePosts, NamePostings populated
)

// InvalidateSymbolCache clears the cached symbol index, forcing a reload
// from disk on the next access. Call after rebuilding the index.
func InvalidateSymbolCache() {
	symIdxMu.Lock()
	symIdxData = nil
	symIdxFiles = nil
	symIdxDir = ""
	symIdxMu.Unlock()
}

func loadSymbolIndexCached(edrDir string) (*Snapshot, []FileEntry) {
	symIdxMu.Lock()
	defer symIdxMu.Unlock()
	if symIdxData != nil && symIdxDir == edrDir {
		return symIdxData, symIdxFiles
	}
	files, symbols, namePosts, namePostings, err := LoadSymbolIndex(edrDir)
	if err != nil {
		return nil, nil
	}
	// Filter out ghost symbols: entries whose byte offsets exceed
	// their file's size (from prior corrupt indices).
	n := 0
	for _, s := range symbols {
		if int(s.FileID) < len(files) && int64(s.EndByte) <= files[s.FileID].Size {
			symbols[n] = s
			n++
		}
	}
	symbols = symbols[:n]

	symIdxDir = edrDir
	symIdxFiles = files
	symIdxData = &Snapshot{
		Symbols:      symbols,
		NamePosts:    namePosts,
		NamePostings: namePostings,
	}
	return symIdxData, symIdxFiles
}

// LookupSymbolsByHash queries symbols by pre-computed name hash.
// Returns nil if no symbol index is available.
func LookupSymbolsByHash(edrDir string, h uint64) []SymbolEntry {
	if !HasSymbolIndex(edrDir) {
		return nil
	}
	d, _ := loadSymbolIndexCached(edrDir)
	if d == nil {
		return nil
	}
	return QuerySymbolsByHash(d, h)
}

// LookupSymbols queries symbols by exact name using the lightweight symbol index.
// Returns nil if no symbol index is available.
func LookupSymbols(edrDir, name string) []SymbolEntry {
	if !HasSymbolIndex(edrDir) {
		return nil
	}
	d, _ := loadSymbolIndexCached(edrDir)
	if d == nil {
		return nil
	}
	return QuerySymbolsByName(d, name)
}

// LookupSymbolsWithIDs returns matching symbols with their index positions.
func LookupSymbolsWithIDs(edrDir, name string) []SymbolWithID {
	if !HasSymbolIndex(edrDir) {
		return nil
	}
	d, _ := loadSymbolIndexCached(edrDir)
	if d == nil {
		return nil
	}
	return QuerySymbolsWithIDs(d, name)
}

// LoadAllSymbols loads all symbols and file entries from the lightweight symbol index.
// Returns nil if no symbol index is available.
func LoadAllSymbols(edrDir string) ([]SymbolEntry, []FileEntry) {
	if !HasSymbolIndex(edrDir) {
		return nil, nil
	}
	d, files := loadSymbolIndexCached(edrDir)
	if d == nil {
		return nil, nil
	}
	return d.Symbols, files
}
