package idx

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"sort"
)

const (
	RefGraphFile    = "refs.bin"
	refGraphMagic   = 0x45524546 // "EREF"
	refGraphVersion = 2          // v2: name-based instead of symbol-to-symbol
)

// RefEdge represents one symbol-to-symbol reference (used only during migration/compat).
type RefEdge struct {
	Caller uint32
	Callee uint32
}

// RefGraphData is the in-memory representation of the reference graph.
// v2 stores references as (symbol → referenced name hashes) with an
// inverted index (name hash → referencing symbol IDs) for fast caller lookups.
type RefGraphData struct {
	NumSymbols uint32

	// Forward index: for each symbol, the name hashes it references.
	// Parallel to the symbol table — ForwardOffsets[i] gives the start
	// in ForwardNames for symbol i; the run ends at ForwardOffsets[i+1].
	ForwardOffsets []uint32 // len = NumSymbols + 1
	ForwardNames   []uint64 // packed referenced name hashes

	// Inverted index: name hash → symbol IDs that reference it.
	// Sorted by name hash for binary search.
	InvEntries []InvEntry
	InvSymIDs  []uint32 // packed symbol IDs
}

// InvEntry is one entry in the inverted index.
type InvEntry struct {
	NameHash uint64
	Offset   uint32 // into InvSymIDs
	Count    uint32
}

// Callers returns the symbol IDs that reference the given symbol ID's name.
func (g *RefGraphData) Callers(calleeID uint32) []uint32 {
	if g == nil || int(calleeID) >= int(g.NumSymbols) {
		return nil
	}
	// We need the callee's name hash. We can get it from the symbol table,
	// but the ref graph doesn't store names. The caller must provide it.
	// This method is kept for backward compat but CallersByName is preferred.
	return nil
}

// CallersByName returns symbol IDs that reference the given name hash.
func (g *RefGraphData) CallersByName(nameHash uint64) []uint32 {
	if g == nil || len(g.InvEntries) == 0 {
		return nil
	}
	// Binary search for the name hash.
	lo := sort.Search(len(g.InvEntries), func(i int) bool {
		return g.InvEntries[i].NameHash >= nameHash
	})
	if lo >= len(g.InvEntries) || g.InvEntries[lo].NameHash != nameHash {
		return nil
	}
	e := g.InvEntries[lo]
	end := e.Offset + e.Count
	if end > uint32(len(g.InvSymIDs)) {
		return nil
	}
	return g.InvSymIDs[e.Offset:end]
}

// Callees returns the symbol IDs referenced by the given symbol ID.
// Resolves name hashes to symbol IDs via the provided name lookup function.
func (g *RefGraphData) Callees(callerID uint32) []uint32 {
	// Kept for backward compat — returns nil for v2 graphs.
	// Use CalleesOf + name resolution instead.
	return nil
}

// CalleeNames returns the name hashes referenced by the given symbol ID.
func (g *RefGraphData) CalleeNames(callerID uint32) []uint64 {
	if g == nil || int(callerID) >= int(g.NumSymbols) {
		return nil
	}
	start := g.ForwardOffsets[callerID]
	end := g.ForwardOffsets[callerID+1]
	if start >= end || end > uint32(len(g.ForwardNames)) {
		return nil
	}
	return g.ForwardNames[start:end]
}

// BuildRefGraphV2 constructs a name-based reference graph from per-symbol
// identifier lists. This is O(symbols × idents) — no edge expansion.
func BuildRefGraphV2(numSymbols uint32, perSymbolIdents [][]string) *RefGraphData {
	// Build forward index: symbol → referenced name hashes
	offsets := make([]uint32, numSymbols+1)
	// First pass: count names per symbol
	totalNames := 0
	for _, idents := range perSymbolIdents {
		totalNames += len(idents)
	}
	names := make([]uint64, 0, totalNames)

	// Inverted: name hash → list of referencing symbol IDs
	invMap := make(map[uint64][]uint32, totalNames/10+1)

	for i, idents := range perSymbolIdents {
		offsets[i] = uint32(len(names))
		seen := make(map[uint64]bool, len(idents))
		for _, ident := range idents {
			h := NameHash(ident)
			if seen[h] {
				continue
			}
			seen[h] = true
			names = append(names, h)
			invMap[h] = append(invMap[h], uint32(i))
		}
	}
	offsets[numSymbols] = uint32(len(names))

	// Build sorted inverted index
	invEntries := make([]InvEntry, 0, len(invMap))
	for h := range invMap {
		invEntries = append(invEntries, InvEntry{NameHash: h})
	}
	sort.Slice(invEntries, func(i, j int) bool {
		return invEntries[i].NameHash < invEntries[j].NameHash
	})

	var invSymIDs []uint32
	for i := range invEntries {
		h := invEntries[i].NameHash
		ids := invMap[h]
		invEntries[i].Offset = uint32(len(invSymIDs))
		invEntries[i].Count = uint32(len(ids))
		invSymIDs = append(invSymIDs, ids...)
	}

	return &RefGraphData{
		NumSymbols:     numSymbols,
		ForwardOffsets: offsets,
		ForwardNames:   names,
		InvEntries:     invEntries,
		InvSymIDs:      invSymIDs,
	}
}

// WriteRefGraph writes the graph to the edr directory.
func WriteRefGraph(edrDir string, g *RefGraphData) error {
	path := filepath.Join(edrDir, RefGraphFile)
	// Estimate size: header(16) + offsets(4*(N+1)) + names(8*M) + inv entries(16*K) + inv IDs(4*L)
	size := 16 +
		4*len(g.ForwardOffsets) +
		8*len(g.ForwardNames) +
		16*len(g.InvEntries) +
		4*len(g.InvSymIDs)
	buf := make([]byte, size)
	pos := 0

	binary.LittleEndian.PutUint32(buf[pos:], refGraphMagic)
	pos += 4
	binary.LittleEndian.PutUint32(buf[pos:], refGraphVersion)
	pos += 4
	binary.LittleEndian.PutUint32(buf[pos:], g.NumSymbols)
	pos += 4
	binary.LittleEndian.PutUint32(buf[pos:], uint32(len(g.InvEntries)))
	pos += 4

	// Forward offsets
	for _, o := range g.ForwardOffsets {
		binary.LittleEndian.PutUint32(buf[pos:], o)
		pos += 4
	}
	// Forward names
	for _, n := range g.ForwardNames {
		binary.LittleEndian.PutUint64(buf[pos:], n)
		pos += 8
	}
	// Inverted entries
	for _, e := range g.InvEntries {
		binary.LittleEndian.PutUint64(buf[pos:], e.NameHash)
		pos += 8
		binary.LittleEndian.PutUint32(buf[pos:], e.Offset)
		pos += 4
		binary.LittleEndian.PutUint32(buf[pos:], e.Count)
		pos += 4
	}
	// Inverted symbol IDs
	for _, id := range g.InvSymIDs {
		binary.LittleEndian.PutUint32(buf[pos:], id)
		pos += 4
	}

	return atomicWrite(path, buf[:pos])
}

// ReadRefGraph loads the reference graph from the edr directory.
// Returns nil if the file does not exist or is corrupt.
func ReadRefGraph(edrDir string) *RefGraphData {
	path := filepath.Join(edrDir, RefGraphFile)
	data, err := os.ReadFile(path)
	if err != nil || len(data) < 16 {
		return nil
	}

	magic := binary.LittleEndian.Uint32(data[0:4])
	version := binary.LittleEndian.Uint32(data[4:8])
	numSymbols := binary.LittleEndian.Uint32(data[8:12])
	numInv := binary.LittleEndian.Uint32(data[12:16])

	if magic != refGraphMagic {
		return nil
	}

	// v1 compat: old symbol-to-symbol format — ignore, will be rebuilt
	if version == 1 {
		return nil
	}
	if version != refGraphVersion {
		return nil
	}

	pos := 16

	// Forward offsets: numSymbols + 1 entries
	nOffsets := int(numSymbols + 1)
	if pos+4*nOffsets > len(data) {
		return nil
	}
	offsets := make([]uint32, nOffsets)
	for i := range offsets {
		offsets[i] = binary.LittleEndian.Uint32(data[pos:])
		pos += 4
	}

	// Forward names: offsets[numSymbols] entries
	nNames := int(offsets[numSymbols])
	if pos+8*nNames > len(data) {
		return nil
	}
	names := make([]uint64, nNames)
	for i := range names {
		names[i] = binary.LittleEndian.Uint64(data[pos:])
		pos += 8
	}

	// Inverted entries
	if pos+16*int(numInv) > len(data) {
		return nil
	}
	invEntries := make([]InvEntry, numInv)
	for i := range invEntries {
		invEntries[i].NameHash = binary.LittleEndian.Uint64(data[pos:])
		pos += 8
		invEntries[i].Offset = binary.LittleEndian.Uint32(data[pos:])
		pos += 4
		invEntries[i].Count = binary.LittleEndian.Uint32(data[pos:])
		pos += 4
	}

	// Inverted symbol IDs: remaining data
	nInvIDs := (len(data) - pos) / 4
	invSymIDs := make([]uint32, nInvIDs)
	for i := range invSymIDs {
		invSymIDs[i] = binary.LittleEndian.Uint32(data[pos:])
		pos += 4
	}

	return &RefGraphData{
		NumSymbols:     numSymbols,
		ForwardOffsets: offsets,
		ForwardNames:   names,
		InvEntries:     invEntries,
		InvSymIDs:      invSymIDs,
	}
}

// HasRefGraph returns true if a reference graph exists in the edr directory.
func HasRefGraph(edrDir string) bool {
	_, err := os.Stat(filepath.Join(edrDir, RefGraphFile))
	return err == nil
}
