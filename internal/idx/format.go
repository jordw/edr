package idx

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
)

// Binary format constants.
var magic = [8]byte{'E', 'D', 'R', 'T', 'R', 'I', 0, 0}

const (
	// version2: trigrams are extracted from lowercased file content,
	// enabling case-insensitive queries. Version 1 used raw bytes.
	currentVersion = 2
	headerSize     = 8 + 4 + 4 + 4 + 8 + 8 + 8 // magic + version + numFiles + numTrigrams + gitMtime + fileTableOff + postingOff = 44
)

// Header is the fixed-size file header.
type Header struct {
	Version      uint32
	NumFiles     uint32
	NumTrigrams  uint32
	GitMtime     int64  // mtime of .git/index at build time (unix nanos)
	FileTableOff uint64 // offset of file table
	PostingOff   uint64 // offset of posting lists
}

// FileEntry is a file in the file table.
type FileEntry struct {
	Path  string
	Mtime int64 // unix nanos
	Size  int64
}

// TrigramEntry is a row in the trigram table.
type TrigramEntry struct {
	Tri    Trigram
	Count  uint32
	Offset uint64 // offset into posting data
}

// IndexData is the fully decoded in-memory representation.
type IndexData struct {
	Header   Header
	Files    []FileEntry
	Trigrams []TrigramEntry
	Postings []byte // raw posting data (delta-varint encoded file IDs per trigram)
}

// Marshal serializes an IndexData to binary format.
func (d *IndexData) Marshal() []byte {
	var buf bytes.Buffer

	// Header
	buf.Write(magic[:])
	binary.Write(&buf, binary.LittleEndian, uint32(currentVersion))
	binary.Write(&buf, binary.LittleEndian, d.Header.NumFiles)
	binary.Write(&buf, binary.LittleEndian, d.Header.NumTrigrams)
	binary.Write(&buf, binary.LittleEndian, d.Header.GitMtime)

	// Placeholder for file table and posting offsets — fill in later
	fileTableOffPos := buf.Len()
	binary.Write(&buf, binary.LittleEndian, uint64(0))
	postingOffPos := buf.Len()
	binary.Write(&buf, binary.LittleEndian, uint64(0))

	// File table
	fileTableOff := uint64(buf.Len())
	for _, f := range d.Files {
		pathBytes := []byte(f.Path)
		binary.Write(&buf, binary.LittleEndian, uint16(len(pathBytes)))
		buf.Write(pathBytes)
		binary.Write(&buf, binary.LittleEndian, f.Mtime)
		binary.Write(&buf, binary.LittleEndian, f.Size)
	}

	// Posting lists
	postingOff := uint64(buf.Len())
	buf.Write(d.Postings)

	// Trigram table (after postings, so we know offsets)
	for _, t := range d.Trigrams {
		buf.Write(t.Tri[:])
		buf.WriteByte(0) // pad
		binary.Write(&buf, binary.LittleEndian, t.Count)
		binary.Write(&buf, binary.LittleEndian, t.Offset)
	}

	// Patch offsets in header
	data := buf.Bytes()
	binary.LittleEndian.PutUint64(data[fileTableOffPos:], fileTableOff)
	binary.LittleEndian.PutUint64(data[postingOffPos:], postingOff)

	return data
}

// Unmarshal decodes binary data into an IndexData.
func Unmarshal(data []byte) (*IndexData, error) {
	if len(data) < headerSize {
		return nil, fmt.Errorf("trigram index too small: %d bytes", len(data))
	}
	if !bytes.Equal(data[:8], magic[:]) {
		return nil, fmt.Errorf("invalid trigram index magic")
	}

	d := &IndexData{}
	d.Header.Version = binary.LittleEndian.Uint32(data[8:12])
	if d.Header.Version != currentVersion {
		return nil, fmt.Errorf("unsupported trigram index version: %d", d.Header.Version)
	}
	d.Header.NumFiles = binary.LittleEndian.Uint32(data[12:16])
	d.Header.NumTrigrams = binary.LittleEndian.Uint32(data[16:20])
	d.Header.GitMtime = int64(binary.LittleEndian.Uint64(data[20:28]))
	d.Header.FileTableOff = binary.LittleEndian.Uint64(data[28:36])
	d.Header.PostingOff = binary.LittleEndian.Uint64(data[36:44])

	// Parse file table
	d.Files = make([]FileEntry, 0, d.Header.NumFiles)
	pos := d.Header.FileTableOff
	for i := uint32(0); i < d.Header.NumFiles; i++ {
		if pos+2 > uint64(len(data)) {
			return nil, fmt.Errorf("truncated file table at entry %d", i)
		}
		pathLen := binary.LittleEndian.Uint16(data[pos:])
		pos += 2
		if pos+uint64(pathLen)+16 > uint64(len(data)) {
			return nil, fmt.Errorf("truncated file table path at entry %d", i)
		}
		path := string(data[pos : pos+uint64(pathLen)])
		pos += uint64(pathLen)
		mtime := int64(binary.LittleEndian.Uint64(data[pos:]))
		pos += 8
		size := int64(binary.LittleEndian.Uint64(data[pos:]))
		pos += 8
		d.Files = append(d.Files, FileEntry{Path: path, Mtime: mtime, Size: size})
	}

	// Posting data is between PostingOff and the trigram table.
	// Trigram table starts after postings and runs to end of file.
	// Each trigram entry is 16 bytes: 3 (tri) + 1 (pad) + 4 (count) + 8 (offset)
	trigramTableSize := uint64(d.Header.NumTrigrams) * 16
	trigramTableOff := uint64(len(data)) - trigramTableSize

	if d.Header.PostingOff <= uint64(len(data)) && trigramTableOff >= d.Header.PostingOff {
		d.Postings = data[d.Header.PostingOff:trigramTableOff]
	}

	// Parse trigram table
	d.Trigrams = make([]TrigramEntry, 0, d.Header.NumTrigrams)
	tpos := trigramTableOff
	for i := uint32(0); i < d.Header.NumTrigrams; i++ {
		if tpos+16 > uint64(len(data)) {
			return nil, fmt.Errorf("truncated trigram table at entry %d", i)
		}
		var te TrigramEntry
		te.Tri = Trigram{data[tpos], data[tpos+1], data[tpos+2]}
		// data[tpos+3] is padding
		te.Count = binary.LittleEndian.Uint32(data[tpos+4:])
		te.Offset = binary.LittleEndian.Uint64(data[tpos+8:])
		d.Trigrams = append(d.Trigrams, te)
		tpos += 16
	}

	return d, nil
}

// BuildPostings builds sorted posting lists for the given trigram→fileID mapping.
// Returns the raw posting bytes and a slice of TrigramEntry with offsets set.
func BuildPostings(triMap map[Trigram][]uint32) ([]byte, []TrigramEntry) {
	// Sort trigrams for binary search
	tris := make([]Trigram, 0, len(triMap))
	for t := range triMap {
		tris = append(tris, t)
	}
	sort.Slice(tris, func(i, j int) bool {
		return tris[i].ToUint32() < tris[j].ToUint32()
	})

	var postings bytes.Buffer
	entries := make([]TrigramEntry, 0, len(tris))

	for _, t := range tris {
		ids := triMap[t]
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		// Deduplicate
		ids = dedup(ids)

		offset := uint64(postings.Len())
		// Delta-varint encode
		prev := uint32(0)
		for _, id := range ids {
			delta := id - prev
			writeVarint(&postings, delta)
			prev = id
		}
		entries = append(entries, TrigramEntry{
			Tri:    t,
			Count:  uint32(len(ids)),
			Offset: offset,
		})
	}

	return postings.Bytes(), entries
}

// DecodePosting decodes a delta-varint encoded posting list.
func DecodePosting(data []byte, offset uint64, count uint32) []uint32 {
	if offset >= uint64(len(data)) {
		return nil
	}
	out := make([]uint32, 0, count)
	pos := offset
	prev := uint32(0)
	for i := uint32(0); i < count; i++ {
		delta, n := readVarint(data[pos:])
		if n == 0 {
			break
		}
		pos += uint64(n)
		prev += delta
		out = append(out, prev)
	}
	return out
}

func writeVarint(buf *bytes.Buffer, v uint32) {
	for v >= 0x80 {
		buf.WriteByte(byte(v) | 0x80)
		v >>= 7
	}
	buf.WriteByte(byte(v))
}

func readVarint(data []byte) (uint32, int) {
	var v uint32
	for i := 0; i < len(data) && i < 5; i++ {
		b := data[i]
		v |= uint32(b&0x7f) << (7 * uint(i))
		if b < 0x80 {
			return v, i + 1
		}
	}
	return 0, 0
}

func dedup(ids []uint32) []uint32 {
	if len(ids) <= 1 {
		return ids
	}
	j := 1
	for i := 1; i < len(ids); i++ {
		if ids[i] != ids[i-1] {
			ids[j] = ids[i]
			j++
		}
	}
	return ids[:j]
}
