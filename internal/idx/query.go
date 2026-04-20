package idx

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

// Query returns candidate file paths that might contain all query trigrams.
// Returns nil, false if no index exists.
func Query(edrDir string, queryTrigrams []Trigram) ([]string, bool) {
	if len(queryTrigrams) == 0 {
		return nil, false
	}

	f, err := os.Open(filepath.Join(edrDir, MainFile))
	if err != nil {
		return nil, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() < int64(v2HeaderSize) {
		return nil, false
	}

	// Mmap the entire file — OS pages in only what we touch.
	data, err := syscall.Mmap(int(f.Fd()), 0, int(fi.Size()),
		syscall.PROT_READ, syscall.MAP_PRIVATE)
	if err != nil {
		return nil, false
	}
	defer syscall.Munmap(data)

	h, err := ReadHeaderBytes(data)
	if err != nil || h.NumTrigrams == 0 {
		return nil, false
	}

	// Locate trigram table.
	trigramTableSize := uint64(h.NumTrigrams) * 16
	var trigramTableOff uint64
	if h.Version >= 3 && h.SymbolOff > 0 {
		trigramTableOff = h.SymbolOff - trigramTableSize
	} else {
		trigramTableOff = uint64(len(data)) - trigramTableSize
	}
	triData := data[trigramTableOff : trigramTableOff+trigramTableSize]

	// Posting data between PostingOff and trigram table.
	var postData []byte
	if h.PostingOff <= uint64(len(data)) && trigramTableOff >= h.PostingOff {
		postData = data[h.PostingOff:trigramTableOff]
	}

	// Binary search + intersect — only touches the pages we need.
	candidates := queryRaw(triData, postData, int(h.NumTrigrams), queryTrigrams)
	if candidates == nil {
		return nil, false
	}

	// Resolve matched file IDs from file table.
	ftData := data[h.FileTableOff:h.PostingOff]
	paths := resolveFileIDsFromTable(ftData, h.NumFiles, candidates)
	sort.Strings(paths)
	return paths, true
}

// queryRaw does trigram lookup + posting intersection on mmap'd bytes.
func queryRaw(triData, postData []byte, numTri int, queryTrigrams []Trigram) []uint32 {
	var lists [][]uint32
	for _, qt := range queryTrigrams {
		i := trigramBinarySearch(triData, numTri, qt)
		if i < 0 {
			return []uint32{} // trigram not in index → no matches
		}
		off := uint64(i) * 16
		count := binary.LittleEndian.Uint32(triData[off+4:])
		postOff := binary.LittleEndian.Uint64(triData[off+8:])
		ids := DecodePosting(postData, postOff, count)
		lists = append(lists, ids)
	}
	if len(lists) == 0 {
		return nil
	}
	sort.Slice(lists, func(i, j int) bool { return len(lists[i]) < len(lists[j]) })
	result := lists[0]
	for _, list := range lists[1:] {
		result = intersect(result, list)
		if len(result) == 0 {
			return result
		}
	}
	return result
}

// trigramBinarySearch finds a trigram in the raw 16-byte-entry table.
func trigramBinarySearch(triData []byte, n int, t Trigram) int {
	target := t.ToUint32()
	lo, hi := 0, n-1
	for lo <= hi {
		mid := (lo + hi) / 2
		off := mid * 16
		v := uint32(triData[off])<<16 | uint32(triData[off+1])<<8 | uint32(triData[off+2])
		if v == target {
			return mid
		}
		if v < target {
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return -1
}

// resolveFileIDsFromTable extracts paths for given file IDs from a
// file table byte slice (starting at offset 0 within the slice).
func resolveFileIDsFromTable(ftData []byte, numFiles uint32, ids []uint32) []string {
	need := make(map[uint32]bool, len(ids))
	for _, id := range ids {
		need[id] = true
	}
	paths := make([]string, 0, len(ids))
	pos := uint64(0)
	for i := uint32(0); i < numFiles; i++ {
		if pos+2 > uint64(len(ftData)) {
			break
		}
		pathLen := binary.LittleEndian.Uint16(ftData[pos:])
		pos += 2
		if pos+uint64(pathLen)+16 > uint64(len(ftData)) {
			break
		}
		if need[i] {
			paths = append(paths, string(ftData[pos:pos+uint64(pathLen)]))
		}
		pos += uint64(pathLen) + 16 // skip path + mtime(8) + size(8)
	}
	return paths
}

func intersect(a, b []uint32) []uint32 {
	out := make([]uint32, 0, len(a))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if a[i] == b[j] {
			out = append(out, a[i])
			i++
			j++
		} else if a[i] < b[j] {
			i++
		} else {
			j++
		}
	}
	return out
}
