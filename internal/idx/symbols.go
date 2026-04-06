package idx

import (
	"hash/fnv"
	"sort"
	"strings"
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
func QuerySymbolsByName(d *IndexData, name string) []SymbolEntry {
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

// AllIndexedSymbols returns all symbols from a loaded index.
func AllIndexedSymbols(d *IndexData) []SymbolEntry {
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

// LookupSymbols loads the index and queries symbols by exact name.
// Returns nil if no symbol index is available.
func LookupSymbols(edrDir, name string) []SymbolEntry {
	if !HasSymbolIndex(edrDir) {
		return nil
	}
	d := loadIndex(edrDir)
	if d == nil {
		return nil
	}
	return QuerySymbolsByName(d, name)
}

// LoadAllSymbols loads all symbols from the index.
// Returns nil if no symbol index is available.
func LoadAllSymbols(edrDir string) ([]SymbolEntry, []FileEntry) {
	if !HasSymbolIndex(edrDir) {
		return nil, nil
	}
	d := loadIndex(edrDir)
	if d == nil {
		return nil, nil
	}
	return d.Symbols, d.Files
}
