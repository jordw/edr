package idx

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// Binary format constants.
var magic = [8]byte{'E', 'D', 'R', 'T', 'R', 'I', 0, 0}

// ReadHeader reads only the fixed-size header from the index file,
// avoiding the cost of loading and parsing the full file table and postings.
func ReadHeader(edrDir string) (*Header, error) {
	f, err := os.Open(filepath.Join(edrDir, MainFile))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, headerSize)
	n, err := io.ReadAtLeast(f, buf, v2HeaderSize)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(buf[:8], magic[:]) {
		return nil, fmt.Errorf("invalid trigram index magic")
	}
	version := binary.LittleEndian.Uint32(buf[8:12])
	if version != 2 && version != currentVersion {
		return nil, fmt.Errorf("unsupported trigram index version: %d", version)
	}
	h := &Header{
		Version:      version,
		NumFiles:     binary.LittleEndian.Uint32(buf[12:16]),
		NumTrigrams:  binary.LittleEndian.Uint32(buf[16:20]),
		GitMtime:     int64(binary.LittleEndian.Uint64(buf[20:28])),
		FileTableOff: binary.LittleEndian.Uint64(buf[28:36]),
		PostingOff:   binary.LittleEndian.Uint64(buf[36:44]),
	}
	// v3 extended fields
	if version >= 3 && n >= headerSize {
		h.NumSymbols = binary.LittleEndian.Uint32(buf[44:48])
		h.SymbolOff = binary.LittleEndian.Uint64(buf[48:56])
		h.NamePostOff = binary.LittleEndian.Uint64(buf[56:64])
		h.NumNameKeys = binary.LittleEndian.Uint32(buf[64:68])
	}
	return h, nil
}

const (
	// version3: adds symbol table and name postings for direct symbol lookup.
	// version2: trigrams extracted from lowercased content. Version 1 used raw bytes.
	currentVersion = 3
	v2HeaderSize   = 8 + 4 + 4 + 4 + 8 + 8 + 8 // 44 bytes (v2 compat)
	headerSize     = v2HeaderSize + 4 + 8 + 8 + 4 // + numSymbols + symbolOff + namePostOff + numNameKeys = 68
)

// Header is the fixed-size file header.
type Header struct {
	Version      uint32
	NumFiles     uint32
	NumTrigrams  uint32
	GitMtime     int64  // mtime of .git/index at build time (unix nanos)
	FileTableOff uint64 // offset of file table
	PostingOff   uint64 // offset of posting lists
	// v3 fields: symbol index
	NumSymbols   uint32
	SymbolOff    uint64 // offset of symbol table
	NamePostOff  uint64 // offset of name posting lists
	NumNameKeys  uint32 // number of name posting entries
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
// SymbolKind encodes symbol type as a byte for compact storage.
type SymbolKind uint8

const (
	KindFunction  SymbolKind = 0
	KindMethod    SymbolKind = 1
	KindStruct    SymbolKind = 2
	KindClass     SymbolKind = 3
	KindInterface SymbolKind = 4
	KindType      SymbolKind = 5
	KindVariable  SymbolKind = 6
	KindConstant  SymbolKind = 7
	KindEnum      SymbolKind = 8
	KindImpl      SymbolKind = 9
)

var kindToString = map[SymbolKind]string{
	KindFunction: "function", KindMethod: "method", KindStruct: "struct",
	KindClass: "class", KindInterface: "interface", KindType: "type",
	KindVariable: "variable", KindConstant: "constant", KindEnum: "enum",
	KindImpl: "impl",
}

var stringToKind = map[string]SymbolKind{
	"function": KindFunction, "method": KindMethod, "struct": KindStruct,
	"class": KindClass, "interface": KindInterface, "type": KindType,
	"variable": KindVariable, "constant": KindConstant, "enum": KindEnum,
	"impl": KindImpl,
}

func (k SymbolKind) String() string {
	if s, ok := kindToString[k]; ok { return s }
	return "unknown"
}

func ParseKind(s string) SymbolKind {
	if k, ok := stringToKind[s]; ok { return k }
	return KindFunction
}

// SymbolEntry is a symbol in the symbol table.
type SymbolEntry struct {
	FileID    uint32
	Name      string
	Kind      SymbolKind
	StartLine uint32
	EndLine   uint32
	StartByte uint32
	EndByte   uint32
}

// NamePostEntry maps a name hash to a posting list of symbol IDs.
type NamePostEntry struct {
	NameHash uint64 // FNV-1a hash of lowercased name
	Count    uint32
	Offset   uint64 // offset into NamePostings
}

type IndexData struct {
	Header       Header
	Files        []FileEntry
	Trigrams     []TrigramEntry
	Postings     []byte // raw posting data (delta-varint encoded file IDs per trigram)
	Symbols      []SymbolEntry
	NamePosts    []NamePostEntry
	NamePostings []byte // raw posting data (symbol IDs per name hash)
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

	// Placeholder for offsets — fill in later
	fileTableOffPos := buf.Len()
	binary.Write(&buf, binary.LittleEndian, uint64(0))
	postingOffPos := buf.Len()
	binary.Write(&buf, binary.LittleEndian, uint64(0))
	// v3 placeholders
	binary.Write(&buf, binary.LittleEndian, d.Header.NumSymbols)
	symbolOffPos := buf.Len()
	binary.Write(&buf, binary.LittleEndian, uint64(0))
	namePostOffPos := buf.Len()
	binary.Write(&buf, binary.LittleEndian, uint64(0))
	binary.Write(&buf, binary.LittleEndian, d.Header.NumNameKeys)

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

	// Symbol table (v3)
	symbolOff := uint64(buf.Len())
	for _, s := range d.Symbols {
		binary.Write(&buf, binary.LittleEndian, s.FileID)
		nameBytes := []byte(s.Name)
		binary.Write(&buf, binary.LittleEndian, uint16(len(nameBytes)))
		buf.Write(nameBytes)
		buf.WriteByte(byte(s.Kind))
		binary.Write(&buf, binary.LittleEndian, s.StartLine)
		binary.Write(&buf, binary.LittleEndian, s.EndLine)
		binary.Write(&buf, binary.LittleEndian, s.StartByte)
		binary.Write(&buf, binary.LittleEndian, s.EndByte)
	}

	// Name posting lists (v3)
	namePostOff := uint64(buf.Len())
	buf.Write(d.NamePostings)

	// Name posting table (v3)
	for _, np := range d.NamePosts {
		binary.Write(&buf, binary.LittleEndian, np.NameHash)
		binary.Write(&buf, binary.LittleEndian, np.Count)
		binary.Write(&buf, binary.LittleEndian, np.Offset)
	}

	// Patch offsets in header
	data := buf.Bytes()
	binary.LittleEndian.PutUint64(data[fileTableOffPos:], fileTableOff)
	binary.LittleEndian.PutUint64(data[postingOffPos:], postingOff)
	binary.LittleEndian.PutUint64(data[symbolOffPos:], symbolOff)
	binary.LittleEndian.PutUint64(data[namePostOffPos:], namePostOff)

	return data
}

// Unmarshal decodes binary data into an IndexData.
func Unmarshal(data []byte) (*IndexData, error) {
	if len(data) < v2HeaderSize {
		return nil, fmt.Errorf("trigram index too small: %d bytes", len(data))
	}
	if !bytes.Equal(data[:8], magic[:]) {
		return nil, fmt.Errorf("invalid trigram index magic")
	}

	d := &IndexData{}
	d.Header.Version = binary.LittleEndian.Uint32(data[8:12])
	if d.Header.Version != 2 && d.Header.Version != currentVersion {
		return nil, fmt.Errorf("unsupported trigram index version: %d", d.Header.Version)
	}
	d.Header.NumFiles = binary.LittleEndian.Uint32(data[12:16])
	d.Header.NumTrigrams = binary.LittleEndian.Uint32(data[16:20])
	d.Header.GitMtime = int64(binary.LittleEndian.Uint64(data[20:28]))
	d.Header.FileTableOff = binary.LittleEndian.Uint64(data[28:36])
	d.Header.PostingOff = binary.LittleEndian.Uint64(data[36:44])
	if d.Header.Version >= 3 && len(data) >= headerSize {
		d.Header.NumSymbols = binary.LittleEndian.Uint32(data[44:48])
		d.Header.SymbolOff = binary.LittleEndian.Uint64(data[48:56])
		d.Header.NamePostOff = binary.LittleEndian.Uint64(data[56:64])
		d.Header.NumNameKeys = binary.LittleEndian.Uint32(data[64:68])
	}

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
	// For v3, trigram table ends at symbol table offset. For v2, it's at end of file.
	trigramTableSize := uint64(d.Header.NumTrigrams) * 16
	var trigramTableOff uint64
	if d.Header.Version >= 3 && d.Header.SymbolOff > 0 {
		trigramTableOff = d.Header.SymbolOff - trigramTableSize
	} else {
		trigramTableOff = uint64(len(data)) - trigramTableSize
	}

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

	// Parse symbol table (v3)
	if d.Header.Version >= 3 && d.Header.NumSymbols > 0 && d.Header.SymbolOff > 0 {
		spos := d.Header.SymbolOff
		d.Symbols = make([]SymbolEntry, 0, d.Header.NumSymbols)
		for i := uint32(0); i < d.Header.NumSymbols; i++ {
			if spos+6 > uint64(len(data)) {
				break
			}
			fileID := binary.LittleEndian.Uint32(data[spos:])
			spos += 4
			nameLen := binary.LittleEndian.Uint16(data[spos:])
			spos += 2
			if spos+uint64(nameLen)+17 > uint64(len(data)) {
				break
			}
			name := string(data[spos : spos+uint64(nameLen)])
			spos += uint64(nameLen)
			kind := SymbolKind(data[spos])
			spos++
			startLine := binary.LittleEndian.Uint32(data[spos:])
			spos += 4
			endLine := binary.LittleEndian.Uint32(data[spos:])
			spos += 4
			startByte := binary.LittleEndian.Uint32(data[spos:])
			spos += 4
			endByte := binary.LittleEndian.Uint32(data[spos:])
			spos += 4
			d.Symbols = append(d.Symbols, SymbolEntry{
				FileID: fileID, Name: name, Kind: kind,
				StartLine: startLine, EndLine: endLine,
				StartByte: startByte, EndByte: endByte,
			})
		}

		// Parse name posting data and table
		if d.Header.NamePostOff > 0 && d.Header.NumNameKeys > 0 {
			namePostTableSize := uint64(d.Header.NumNameKeys) * 20 // 8 + 4 + 8 per entry
			namePostTableOff := uint64(len(data)) - namePostTableSize
			if d.Header.NamePostOff <= namePostTableOff {
				d.NamePostings = data[d.Header.NamePostOff:namePostTableOff]
			}
			npos := namePostTableOff
			d.NamePosts = make([]NamePostEntry, 0, d.Header.NumNameKeys)
			for i := uint32(0); i < d.Header.NumNameKeys; i++ {
				if npos+20 > uint64(len(data)) {
					break
				}
				np := NamePostEntry{
					NameHash: binary.LittleEndian.Uint64(data[npos:]),
					Count:    binary.LittleEndian.Uint32(data[npos+8:]),
					Offset:   binary.LittleEndian.Uint64(data[npos+12:]),
				}
				d.NamePosts = append(d.NamePosts, np)
				npos += 20
			}
		}
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
