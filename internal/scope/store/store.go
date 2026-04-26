// Package store persists per-file scope.Result data to a single file
// in the .edr directory and serves point queries without materializing
// the whole index.
//
// File layout (v6):
//
//	[u32 LE: header length in bytes]
//	[gob-encoded Header struct]   ← includes Records map AND string Pool
//	[record body] x N (each body is flate(gob(wireResult)); concatenated,
//	                  no framing — Length in header is on-disk bytes)
//
// The Header carries Records (path -> RecordMeta) and Pool (a []string
// dictionary). Records are encoded as wireResult, where every Name,
// Namespace, Kind, Reason, and Signature is replaced by a uint32 index
// into Pool. Each gob-encoded wireResult is then flate-compressed
// (raw deflate, no gzip header) before being written.
//
// fromWire resolves pool IDs back to strings on decode.
//
// Loading reads only the header (which includes the pool); ResultFor
// seeks+decodes a single record. AllResults materializes every record
// on first call and caches.
//
// Wire stripping continues from v3: Decl/Ref still don't carry File on
// the wire — re-filled from parent wireResult.File on decode.
//
// v6 adds RecordMeta.Size. Prior versions mtime-only-checked the file
// in ResultFor; if the filesystem returned the same mtime after a
// silent-replace (same-second rewrite, explicit touch, snapshot
// restore) the store happily served stale scope data. The staleness
// package now enforces mtime+size, but requires the Size at rest.
//
// Old versions are not read; Load returns an error and callers fall
// back to "no index" until the next rebuild.
package store

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	atomicio "github.com/jordw/edr/internal/atomic"
	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/c"
	"github.com/jordw/edr/internal/scope/cpp"
	"github.com/jordw/edr/internal/scope/csharp"
	"github.com/jordw/edr/internal/scope/golang"
	"github.com/jordw/edr/internal/scope/java"
	"github.com/jordw/edr/internal/scope/kotlin"
	"github.com/jordw/edr/internal/scope/lua"
	"github.com/jordw/edr/internal/scope/php"
	"github.com/jordw/edr/internal/scope/python"
	"github.com/jordw/edr/internal/scope/ruby"
	"github.com/jordw/edr/internal/scope/rust"
	"github.com/jordw/edr/internal/scope/swift"
	"github.com/jordw/edr/internal/scope/ts"
	"github.com/jordw/edr/internal/scope/zig"
	"github.com/jordw/edr/internal/staleness"
)

const (
	// v7: wireDecl gained Exported (import graph, Phase 1). A pre-v7
	// scope.bin is rejected by Load, which callers treat as "no index"
	// and trigger a rebuild on the next `edr index`.
	currentVersion uint32 = 7
	storeFileName         = "scope.bin"
)

// compressBody flate-compresses a record body. Uses raw deflate (no
// gzip header / checksum) since records are locally generated and
// rebuilt on corruption.
func compressBody(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.DefaultCompression)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(raw); err != nil {
		w.Close()
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decompressBody decompresses a record body written by compressBody.
func decompressBody(body []byte) ([]byte, error) {
	r := flate.NewReader(bytes.NewReader(body))
	defer r.Close()
	return io.ReadAll(r)
}

// poolBuilder accumulates a deduplicated string pool during Build.
// ID 0 is reserved for the empty string so empty fields encode as a
// single zero byte (gob varint).
type poolBuilder struct {
	strings []string
	index   map[string]uint32
}

func newPoolBuilder() *poolBuilder {
	return &poolBuilder{
		strings: []string{""},
		index:   map[string]uint32{"": 0},
	}
}

func (p *poolBuilder) intern(s string) uint32 {
	if id, ok := p.index[s]; ok {
		return id
	}
	id := uint32(len(p.strings))
	p.strings = append(p.strings, s)
	p.index[s] = id
	return id
}

// wireResult is the on-disk encoding of scope.Result. Decl/Ref File
// fields are stripped (re-filled from File on decode). Strings are
// interned into Header.Pool and encoded as uint32 IDs.
type wireResult struct {
	File   string
	Scopes []scope.Scope
	Decls  []wireDecl
	Refs   []wireRef
}

type wireDecl struct {
	ID       scope.DeclID
	LocID    scope.LocID
	NameID   uint32
	NsID     uint32
	KindID   uint32
	Scope    scope.ScopeID
	Span     scope.Span
	FullSpan scope.Span
	SigID    uint32
	Exported bool
}

type wireRef struct {
	LocID     scope.LocID
	Span      scope.Span
	NameID    uint32
	NsID      uint32
	Scope     scope.ScopeID
	BindKind  scope.BindingKind
	BindDecl  scope.DeclID
	BindCands []scope.DeclID
	ReasonID  uint32
}

func toWire(r *scope.Result, p *poolBuilder) *wireResult {
	w := &wireResult{
		File:   r.File,
		Scopes: r.Scopes,
		Decls:  make([]wireDecl, len(r.Decls)),
		Refs:   make([]wireRef, len(r.Refs)),
	}
	for i, d := range r.Decls {
		w.Decls[i] = wireDecl{
			ID:       d.ID,
			LocID:    d.LocID,
			NameID:   p.intern(d.Name),
			NsID:     p.intern(string(d.Namespace)),
			KindID:   p.intern(string(d.Kind)),
			Scope:    d.Scope,
			Span:     d.Span,
			FullSpan: d.FullSpan,
			SigID:    p.intern(d.Signature),
			Exported: d.Exported,
		}
	}
	for i, ref := range r.Refs {
		w.Refs[i] = wireRef{
			LocID:     ref.LocID,
			Span:      ref.Span,
			NameID:    p.intern(ref.Name),
			NsID:      p.intern(string(ref.Namespace)),
			Scope:     ref.Scope,
			BindKind:  ref.Binding.Kind,
			BindDecl:  ref.Binding.Decl,
			BindCands: ref.Binding.Candidates,
			ReasonID:  p.intern(ref.Binding.Reason),
		}
	}
	return w
}

func fromWire(w *wireResult, pool []string) *scope.Result {
	get := func(id uint32) string {
		if int(id) >= len(pool) {
			return ""
		}
		return pool[id]
	}
	r := &scope.Result{
		File:   w.File,
		Scopes: w.Scopes,
		Decls:  make([]scope.Decl, len(w.Decls)),
		Refs:   make([]scope.Ref, len(w.Refs)),
	}
	for i, d := range w.Decls {
		r.Decls[i] = scope.Decl{
			ID:        d.ID,
			LocID:     d.LocID,
			Name:      get(d.NameID),
			Namespace: scope.Namespace(get(d.NsID)),
			Kind:      scope.DeclKind(get(d.KindID)),
			Scope:     d.Scope,
			File:      w.File,
			Span:      d.Span,
			FullSpan:  d.FullSpan,
			Signature: get(d.SigID),
			Exported:  d.Exported,
		}
	}
	for i, ref := range w.Refs {
		r.Refs[i] = scope.Ref{
			LocID:     ref.LocID,
			File:      w.File,
			Span:      ref.Span,
			Name:      get(ref.NameID),
			Namespace: scope.Namespace(get(ref.NsID)),
			Scope:     ref.Scope,
			Binding: scope.RefBinding{
				Kind:       ref.BindKind,
				Decl:       ref.BindDecl,
				Candidates: ref.BindCands,
				Reason:     get(ref.ReasonID),
			},
		}
	}
	return r
}

// Header is the index header. Stored as a gob blob immediately after a
// u32 length prefix. Records is keyed by repo-relative file path. Pool
// is the string dictionary; record bodies reference it by index.
type Header struct {
	Version uint32
	Records map[string]RecordMeta
	Pool    []string
}

// RecordMeta locates and dates one per-file scope.Result body within the
// store file. Offset is the absolute byte offset from the start of the
// file; Length is the number of bytes of the record's gob body. Mtime
// is the source file's mod time (unix nanos) at index time, used for
// cheap staleness checks before the record is decoded.
type RecordMeta struct {
	Offset uint64
	Length uint32
	Mtime  int64
	Size   int64 // source file size at index time; paired with Mtime by staleness.IsFresh
}

// Index is a handle to a loaded scope index. It holds the parsed header
// (including the string pool) and an open *os.File for ResultFor
// seek+decode. Close releases the fd; callers that don't (most don't,
// since CLI processes are short-lived) will leak it for the process
// lifetime — best-effort.
type Index struct {
	file   *os.File
	header *Header
	cached map[string]*scope.Result
}

// Parse dispatches to the right language scope builder based on file
// extension. Unsupported extensions return nil (callers should treat
// nil as "no scope info available", not an error).
func Parse(relPath string, src []byte) *scope.Result {
	ext := strings.ToLower(filepath.Ext(relPath))
	switch ext {
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts":
		return ts.Parse(relPath, src)
	case ".go":
		return golang.Parse(relPath, src)
	case ".py", ".pyi":
		return python.Parse(relPath, src)
	case ".java":
		return java.Parse(relPath, src)
	case ".rs":
		return rust.Parse(relPath, src)
	case ".rb":
		return ruby.Parse(relPath, src)
	case ".c", ".h":
		return c.Parse(relPath, src)
	case ".cpp", ".cc", ".cxx", ".hpp", ".hxx", ".hh":
		return cpp.Parse(relPath, src)
	case ".cs":
		return csharp.Parse(relPath, src)
	case ".swift":
		return swift.Parse(relPath, src)
	case ".kt", ".kts":
		return kotlin.Parse(relPath, src)
	case ".php":
		return php.Parse(relPath, src)
	case ".lua":
		return lua.Parse(relPath, src)
	case ".zig":
		return zig.Parse(relPath, src)
	}
	return nil
}

// Exists reports whether a persisted scope index lives in edrDir.
func Exists(edrDir string) bool {
	_, err := os.Stat(filepath.Join(edrDir, storeFileName))
	return err == nil
}

// parsedFile holds a parsed Result in memory during Build's first
// pass so reconcileResults can rewrite DeclIDs before encoding.
type parsedFile struct {
	rel    string
	result *scope.Result
	mtime  int64
	size   int64
}

// Build walks the repo, parses every supported source file, and writes
// the result to edrDir/scope.bin atomically. Returns the number of
// records written. walkFn is the same shape used elsewhere in edr —
// pass walk.RepoFiles for the standard gitignore-aware walk.
func Build(root, edrDir string, walkFn func(string, func(string) error) error) (int, error) {
	if err := os.MkdirAll(edrDir, 0o755); err != nil {
		return 0, fmt.Errorf("scope store: mkdir %s: %w", edrDir, err)
	}

	// First pass: parse every supported file and hold Results in memory
	// so cross-file reconciliation (partial classes, Ruby reopens, TS
	// merging across modules) can rewrite DeclIDs before encoding.
	var parsed []parsedFile

	walkErr := walkFn(root, func(absPath string) error {
		rel, err := filepath.Rel(root, absPath)
		if err != nil {
			return nil
		}
		if strings.HasPrefix(rel, ".edr"+string(filepath.Separator)) || rel == ".edr" {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(rel))
		switch ext {
		case ".ts", ".tsx", ".js", ".jsx", ".mts", ".cts",
			".go", ".py", ".pyi",
			".java", ".rs", ".rb",
			".c", ".h",
			".cpp", ".cc", ".cxx", ".hpp", ".hxx", ".hh",
			".cs", ".swift", ".kt", ".kts", ".php",
			".lua", ".zig":
		default:
			return nil
		}
		src, err := os.ReadFile(absPath)
		if err != nil {
			return nil
		}
		info, err := os.Stat(absPath)
		if err != nil {
			return nil
		}
		result := Parse(rel, src)
		if result == nil {
			return nil
		}
		parsed = append(parsed, parsedFile{
			rel:    rel,
			result: result,
			mtime:  info.ModTime().UnixNano(),
			size:   info.Size(),
		})
		return nil
	})
	if walkErr != nil {
		return 0, walkErr
	}

	// Cross-file declaration merging (C# partial classes, Ruby open-
	// class reopening, TS declaration merging). Mutates parsed in place.
	reconcileResults(parsed)

	// Phase 1 TS import graph: rewrite refs bound to local KindImport
	// decls so they target the exported source-file decl. Runs after
	// reconcileResults so merged DeclIDs are used. Other languages'
	// Results are skipped internally. Mutates parsed in place.
	resolveImports(parsed, root)

	// Second pass: encode each reconciled Result. The string pool is
	// built here so that interning reflects the final (post-merge)
	// identifier names, with one pool shared across all records.
	pool := newPoolBuilder()
	type encodedRecord struct {
		rel   string
		body  []byte
		mtime int64
		size  int64
	}
	records := make([]encodedRecord, 0, len(parsed))
	for _, p := range parsed {
		var buf bytes.Buffer
		if err := gob.NewEncoder(&buf).Encode(toWire(p.result, pool)); err != nil {
			return 0, fmt.Errorf("scope store: encode %s: %w", p.rel, err)
		}
		compressed, err := compressBody(buf.Bytes())
		if err != nil {
			return 0, fmt.Errorf("scope store: compress %s: %w", p.rel, err)
		}
		records = append(records, encodedRecord{
			rel:   p.rel,
			body:  compressed,
			mtime: p.mtime,
			size:  p.size,
		})
	}

	header := &Header{
		Version: currentVersion,
		Records: make(map[string]RecordMeta, len(records)),
		Pool:    pool.strings,
	}
	for _, r := range records {
		header.Records[r.rel] = RecordMeta{
			Offset: 0,
			Length: uint32(len(r.body)),
			Mtime:  r.mtime,
			Size:   r.size,
		}
	}
	headerBytes, err := encodeHeader(header)
	if err != nil {
		return 0, err
	}

	// Compute real offsets and re-encode header to a fixed point. Gob
	// varint width depends on the value, so non-zero offsets can change
	// the header size; iterate until stable. Converges in 1-2 rounds.
	dataStart := uint64(4 + len(headerBytes))
	for iter := 0; iter < 4; iter++ {
		offset := dataStart
		for _, r := range records {
			meta := header.Records[r.rel]
			meta.Offset = offset
			header.Records[r.rel] = meta
			offset += uint64(len(r.body))
		}
		newHeaderBytes, err := encodeHeader(header)
		if err != nil {
			return 0, err
		}
		newDataStart := uint64(4 + len(newHeaderBytes))
		if newDataStart == dataStart {
			headerBytes = newHeaderBytes
			break
		}
		dataStart = newDataStart
		headerBytes = newHeaderBytes
	}

	finalPath := filepath.Join(edrDir, storeFileName)
	if err := atomicio.WriteVia(finalPath, func(w io.Writer) error {
		var prefix [4]byte
		binary.LittleEndian.PutUint32(prefix[:], uint32(len(headerBytes)))
		if _, err := w.Write(prefix[:]); err != nil {
			return fmt.Errorf("scope store: write prefix: %w", err)
		}
		if _, err := w.Write(headerBytes); err != nil {
			return fmt.Errorf("scope store: write header: %w", err)
		}
		for _, r := range records {
			if _, err := w.Write(r.body); err != nil {
				return fmt.Errorf("scope store: write record %s: %w", r.rel, err)
			}
		}
		return nil
	}); err != nil {
		return 0, err
	}
	return len(records), nil
}

// Load opens edrDir/scope.bin and decodes only the header (which
// includes the string pool). Returns (nil, nil) if the file doesn't
// exist (preserves the original behavior: callers ignore "not built
// yet" and fall back to live parses).
//
// On any other error (truncated file, version mismatch, gob decode
// failure) Load returns the error. Callers in dispatch_refs.go discard
// errors with `if idx, _ := Load(...); idx != nil`, so an old-format
// file simply degrades to "no index" until the next `edr index` rebuild.
func Load(edrDir string) (*Index, error) {
	path := filepath.Join(edrDir, storeFileName)
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("scope store: open %s: %w", path, err)
	}

	var prefix [4]byte
	if _, err := io.ReadFull(f, prefix[:]); err != nil {
		f.Close()
		return nil, fmt.Errorf("scope store: read header length: %w", err)
	}
	headerLen := binary.LittleEndian.Uint32(prefix[:])
	if headerLen == 0 {
		f.Close()
		return nil, fmt.Errorf("scope store: empty header")
	}
	headerBytes := make([]byte, headerLen)
	if _, err := io.ReadFull(f, headerBytes); err != nil {
		f.Close()
		return nil, fmt.Errorf("scope store: read header: %w", err)
	}
	header := &Header{}
	if err := gob.NewDecoder(bytes.NewReader(headerBytes)).Decode(header); err != nil {
		f.Close()
		return nil, fmt.Errorf("scope store: decode header: %w", err)
	}
	if header.Version != currentVersion {
		f.Close()
		return nil, fmt.Errorf("scope store: unsupported version %d (want %d)", header.Version, currentVersion)
	}
	return &Index{file: f, header: header}, nil
}

// Close releases the underlying file descriptor. Callers in the existing
// codebase don't call Close — that's fine; the process exits shortly after
// any CLI invocation and the kernel reclaims the fd. Best-effort.
func (idx *Index) Close() error {
	if idx == nil || idx.file == nil {
		return nil
	}
	err := idx.file.Close()
	idx.file = nil
	return err
}

// ResultFor returns the cached scope.Result for relPath, or nil if the
// index doesn't have it OR the on-disk source file's mtime no longer
// matches the index entry (stale). Decodes a single record via ReadAt;
// does NOT materialize the full map.
//
// root is the repo root so we can resolve relPath -> absolute path for
// the staleness check. relPath should match what Build wrote (filepath.Rel
// from root, OS-native separators).
func (idx *Index) ResultFor(root, relPath string) *scope.Result {
	if idx == nil || idx.header == nil || idx.file == nil {
		return nil
	}
	meta, ok := idx.header.Records[relPath]
	if !ok {
		return nil
	}
	// mtime+size check — silent-replace (same mtime, different
	// content) must count as stale. Prior versions checked only
	// mtime and happily served stale data through same-second
	// rewrites, explicit touch, or snapshot restore.
	if !staleness.IsFresh(root, staleness.Entry{
		Path:  relPath,
		Mtime: meta.Mtime,
		Size:  meta.Size,
	}) {
		return nil
	}
	if idx.cached != nil {
		if r, ok := idx.cached[relPath]; ok {
			return r
		}
	}
	body := make([]byte, meta.Length)
	if _, err := idx.file.ReadAt(body, int64(meta.Offset)); err != nil {
		return nil
	}
	raw, err := decompressBody(body)
	if err != nil {
		return nil
	}
	var w wireResult
	if err := gob.NewDecoder(bytes.NewReader(raw)).Decode(&w); err != nil {
		return nil
	}
	return fromWire(&w, idx.header.Pool)
}

// AllResults returns every record in the index, decoding on first call
// and caching for subsequent calls. This preserves the v1 semantics for
// callers that genuinely need a full scan (dispatch_refs.go cross-file
// walks); they still pay the full deserialization cost, but only when
// they actually ask for it.
//
// Stale records (file mtime differs from index) are still returned —
// AllResults is for "give me everything you have"; cross-file binding
// callers can re-parse individually if precision matters.
func (idx *Index) AllResults() map[string]*scope.Result {
	if idx == nil || idx.header == nil || idx.file == nil {
		return nil
	}
	if idx.cached != nil {
		return idx.cached
	}
	out := make(map[string]*scope.Result, len(idx.header.Records))
	for rel, meta := range idx.header.Records {
		body := make([]byte, meta.Length)
		if _, err := idx.file.ReadAt(body, int64(meta.Offset)); err != nil {
			continue
		}
		raw, err := decompressBody(body)
		if err != nil {
			continue
		}
		var w wireResult
		if err := gob.NewDecoder(bytes.NewReader(raw)).Decode(&w); err != nil {
			continue
		}
		out[rel] = fromWire(&w, idx.header.Pool)
	}
	idx.cached = out
	return out
}

// encodeHeader gob-encodes a Header into a byte slice.
func encodeHeader(h *Header) ([]byte, error) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(h); err != nil {
		return nil, fmt.Errorf("scope store: encode header: %w", err)
	}
	return buf.Bytes(), nil
}
