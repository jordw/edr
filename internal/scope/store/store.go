// Package store persists per-file scope.Result data to a single file
// in the .edr directory and serves point queries without materializing
// the whole index.
//
// File layout (v5):
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

	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/c"
	"github.com/jordw/edr/internal/scope/cpp"
	"github.com/jordw/edr/internal/scope/csharp"
	"github.com/jordw/edr/internal/scope/golang"
	"github.com/jordw/edr/internal/scope/java"
	"github.com/jordw/edr/internal/scope/kotlin"
	"github.com/jordw/edr/internal/scope/php"
	"github.com/jordw/edr/internal/scope/python"
	"github.com/jordw/edr/internal/scope/ruby"
	"github.com/jordw/edr/internal/scope/rust"
	"github.com/jordw/edr/internal/scope/swift"
	"github.com/jordw/edr/internal/scope/ts"
)

const (
	currentVersion uint32 = 5
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
	case ".ts", ".tsx", ".js", ".jsx", ".mts", ".cts":
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
	}
	return nil
}

// Exists reports whether a persisted scope index lives in edrDir.
func Exists(edrDir string) bool {
	_, err := os.Stat(filepath.Join(edrDir, storeFileName))
	return err == nil
}

// Build walks the repo, parses every supported source file, and writes
// the result to edrDir/scope.bin atomically. Returns the number of
// records written. walkFn is the same shape used elsewhere in edr —
// pass index.WalkRepoFiles for the standard gitignore-aware walk.
func Build(root, edrDir string, walkFn func(string, func(string) error) error) (int, error) {
	if err := os.MkdirAll(edrDir, 0o755); err != nil {
		return 0, fmt.Errorf("scope store: mkdir %s: %w", edrDir, err)
	}

	// Single pool shared across all records — that's the whole point.
	pool := newPoolBuilder()

	// First pass: parse every supported file, intern its strings into the
	// pool while building wireResult, gob-encode the wireResult into a
	// per-record buffer. Pool is finalized when the walk completes.
	type encodedRecord struct {
		rel   string
		body  []byte
		mtime int64
	}
	var records []encodedRecord

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
			".cs", ".swift", ".kt", ".kts", ".php":
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
		var buf bytes.Buffer
		if err := gob.NewEncoder(&buf).Encode(toWire(result, pool)); err != nil {
			return fmt.Errorf("scope store: encode %s: %w", rel, err)
		}
		compressed, err := compressBody(buf.Bytes())
		if err != nil {
			return fmt.Errorf("scope store: compress %s: %w", rel, err)
		}
		records = append(records, encodedRecord{
			rel:   rel,
			body:  compressed,
			mtime: info.ModTime().UnixNano(),
		})
		return nil
	})
	if walkErr != nil {
		return 0, walkErr
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

	tmp, err := os.CreateTemp(edrDir, "scope-*.bin")
	if err != nil {
		return 0, fmt.Errorf("scope store: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpPath)
	}

	var prefix [4]byte
	binary.LittleEndian.PutUint32(prefix[:], uint32(len(headerBytes)))
	if _, err := tmp.Write(prefix[:]); err != nil {
		cleanup()
		return 0, fmt.Errorf("scope store: write prefix: %w", err)
	}
	if _, err := tmp.Write(headerBytes); err != nil {
		cleanup()
		return 0, fmt.Errorf("scope store: write header: %w", err)
	}
	for _, r := range records {
		if _, err := tmp.Write(r.body); err != nil {
			cleanup()
			return 0, fmt.Errorf("scope store: write record %s: %w", r.rel, err)
		}
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return 0, fmt.Errorf("scope store: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return 0, fmt.Errorf("scope store: close temp: %w", err)
	}

	finalPath := filepath.Join(edrDir, storeFileName)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return 0, fmt.Errorf("scope store: rename %s -> %s: %w", tmpPath, finalPath, err)
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
	abs := relPath
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, relPath)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil
	}
	if info.ModTime().UnixNano() != meta.Mtime {
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
