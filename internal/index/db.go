package index

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

// DB is the persistent index database.
type DB struct {
	db       *retryDB
	raw      *sql.DB // underlying sql.DB for Close and pragmas
	root     string
	writeMu  sync.Mutex // in-process writer serialization
	lockFile *os.File   // cross-process writer lock (.edr/writer.lock)
	batchTx  *sql.Tx    // optional: if set, write methods join this tx instead of creating their own

	indexWarnings []FileError // per-file errors from last IndexRepo run
}

// FileError records a per-file failure during indexing.
type FileError struct {
	File  string `json:"file"`
	Phase string `json:"phase"` // "parse", "upsert", "clear", "symbols", "imports", "refs"
	Err   error  `json:"-"`
	Msg   string `json:"error"`
}

// IndexWarnings returns per-file errors from the most recent IndexRepo run.
func (d *DB) IndexWarnings() []FileError { return d.indexWarnings }

// retryDB wraps sql.DB to automatically retry on SQLITE_BUSY errors.
// modernc.org/sqlite's PRAGMA busy_timeout doesn't reliably work across
// separate OS processes, so we implement retry at the Go level.
type retryDB struct {
	db *sql.DB
}

func (r *retryDB) Exec(query string, args ...any) (sql.Result, error) {
	var res sql.Result
	err := retryBusy(func() error {
		var e error
		res, e = r.db.Exec(query, args...)
		return e
	})
	return res, err
}

func (r *retryDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	var res sql.Result
	err := retryBusy(func() error {
		var e error
		res, e = r.db.ExecContext(ctx, query, args...)
		return e
	})
	return res, err
}

func (r *retryDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	var rows *sql.Rows
	err := retryBusy(func() error {
		var e error
		rows, e = r.db.QueryContext(ctx, query, args...)
		return e
	})
	return rows, err
}

// retryRow wraps sql.Row so that Scan retries on SQLITE_BUSY.
// sql.Row defers errors to Scan(), so retry must wrap the full query+scan cycle.
type retryRow struct {
	db    *sql.DB
	ctx   context.Context
	query string
	args  []any
}

func (r *retryRow) Scan(dest ...any) error {
	return retryBusy(func() error {
		return r.db.QueryRowContext(r.ctx, r.query, r.args...).Scan(dest...)
	})
}

func (r *retryDB) QueryRow(query string, args ...any) *retryRow {
	return &retryRow{db: r.db, ctx: context.Background(), query: query, args: args}
}

func (r *retryDB) QueryRowContext(ctx context.Context, query string, args ...any) *retryRow {
	return &retryRow{db: r.db, ctx: ctx, query: query, args: args}
}

func (r *retryDB) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	var stmt *sql.Stmt
	err := retryBusy(func() error {
		var e error
		stmt, e = r.db.PrepareContext(ctx, query)
		return e
	})
	return stmt, err
}

func (r *retryDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	var tx *sql.Tx
	err := retryBusy(func() error {
		var e error
		tx, e = r.db.BeginTx(ctx, opts)
		return e
	})
	return tx, err
}

// OpenDB opens or creates the index database in the .edr directory.
func OpenDB(repoRoot string) (*DB, error) {
	root, err := NormalizeRoot(repoRoot)
	if err != nil {
		return nil, err
	}

	edrDir := filepath.Join(root, ".edr")
	if err := os.MkdirAll(edrDir, 0755); err != nil {
		return nil, fmt.Errorf("create .edr dir: %w", err)
	}

	dbPath := filepath.Join(edrDir, "index.db")
	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	// Set busy_timeout FIRST so subsequent PRAGMAs don't fail with
	// "database is locked" if another process is mid-write.
	// 30s matches the retryBusy deadline so SQLite-level and Go-level
	// retries cooperate instead of the shorter timeout giving up early.
	if _, err := sqlDB.Exec("PRAGMA busy_timeout=30000"); err != nil {
		sqlDB.Close()
		return nil, err
	}
	// Enable WAL mode for better concurrent access.
	// Retry because journal_mode=WAL is a write-like operation that can
	// hit SQLITE_BUSY if another process is mid-transaction.
	if err := retryBusy(func() error {
		_, e := sqlDB.Exec("PRAGMA journal_mode=WAL")
		return e
	}); err != nil {
		sqlDB.Close()
		return nil, err
	}

	// Two connections: one for the batch write transaction (BeginBatch),
	// one for concurrent reads (e.g. GetFileHash during IndexRepo).
	// WAL mode allows readers and writers to proceed concurrently.
	// Write safety is ensured by WithWriteLock (mutex + flock).
	sqlDB.SetMaxOpenConns(2)

	// Open a persistent lock file for cross-process writer serialization.
	// The file stays open for the lifetime of the DB; flock is acquired/released
	// per write operation via WithWriteLock.
	lockPath := filepath.Join(edrDir, "writer.lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("open writer lock: %w", err)
	}

	d := &DB{db: &retryDB{db: sqlDB}, raw: sqlDB, root: root, lockFile: lockFile}
	// Run migrations under the writer lock so that two concurrent first-use
	// processes don't race on schema creation (SQLITE_BUSY).
	if err := d.WithWriteLock(func() error {
		return d.migrate()
	}); err != nil {
		lockFile.Close()
		sqlDB.Close()
		return nil, err
	}
	return d, nil
}

// WithWriteLock serializes index mutations across goroutines (sync.Mutex) and
// across OS processes (flock on .edr/writer.lock). All IndexFile / IndexRepo
// calls should run inside this lock.
func (d *DB) WithWriteLock(fn func() error) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()

	if err := syscall.Flock(int(d.lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquire writer lock: %w", err)
	}
	defer syscall.Flock(int(d.lockFile.Fd()), syscall.LOCK_UN)

	return fn()
}

// execer is the common interface between *sql.DB (via retryDB) and *sql.Tx.
// Write methods use this so they can participate in a batch transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
}

// BeginBatch starts a batch transaction. While active, write methods
// (UpsertFile, ClearFileData, InsertSymbolsBatch, InsertImports, InsertRefs)
// join this transaction instead of creating their own. This collapses
// ~5N transaction commits into 1 when reindexing N files.
// Must be called while holding the writer lock. Call CommitBatch to finalize.
func (d *DB) BeginBatch(ctx context.Context) error {
	if d.batchTx != nil {
		return fmt.Errorf("batch already active")
	}
	tx, err := d.raw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin batch: %w", err)
	}
	d.batchTx = tx
	return nil
}

// CommitBatch commits the active batch transaction.
func (d *DB) CommitBatch() error {
	if d.batchTx == nil {
		return nil
	}
	err := d.batchTx.Commit()
	d.batchTx = nil
	return err
}

// RollbackBatch rolls back the active batch transaction.
func (d *DB) RollbackBatch() {
	if d.batchTx != nil {
		d.batchTx.Rollback()
		d.batchTx = nil
	}
}

// writerExecer returns the batch tx if active, otherwise the retryDB wrapper.
func (d *DB) writerExecer() execer {
	if d.batchTx != nil {
		return d.batchTx
	}
	return d.db
}

// localTxOrBatch returns an execer and a commit function. If a batch tx is
// active, the execer joins it and commitFn is a no-op. Otherwise a new local
// transaction is created; commitFn commits it. On error, call the returned
// rollbackFn (no-op when using batch tx).
func (d *DB) localTxOrBatch(ctx context.Context) (q execer, commitFn func() error, err error) {
	if d.batchTx != nil {
		return d.batchTx, func() error { return nil }, nil
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	return tx, tx.Commit, nil
}

func (d *DB) Close() error {
	if d.lockFile != nil {
		d.lockFile.Close()
	}
	// Passive checkpoint: move committed WAL pages to the main DB file without
	// blocking concurrent readers. Short-lived CLI processes benefit from this
	// to keep the WAL from growing unbounded, but we avoid TRUNCATE which is a
	// high-contention write operation.
	d.raw.Exec("PRAGMA wal_checkpoint(PASSIVE)")
	return d.raw.Close()
}

// isSQLiteBusy returns true if the error is a SQLite busy/locked error.
func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "SQLITE_BUSY") || strings.Contains(s, "database is locked")
}

// retryBusy retries a function up to ~30s on SQLite busy errors.
// modernc.org/sqlite's busy_timeout PRAGMA doesn't reliably work across
// separate OS processes, so we implement retry at the Go level.
func retryBusy(fn func() error) error {
	var err error
	backoff := 10 * time.Millisecond
	deadline := time.Now().Add(30 * time.Second)
	for {
		err = fn()
		if err == nil || !isSQLiteBusy(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(backoff)
		backoff = backoff * 2
		if backoff > 500*time.Millisecond {
			backoff = 500 * time.Millisecond
		}
	}
}


const currentSchemaVersion = 6

func (d *DB) migrate() error {
	// Create schema_version table if it doesn't exist
	_, err := d.db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`)
	if err != nil {
		return err
	}

	var version int
	err = d.db.QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version)
	if err != nil {
		// No version row yet — check if tables exist from v0 (pre-versioning)
		var count int
		_ = d.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='symbols'").Scan(&count)
		if count > 0 {
			version = 1 // existing DB without version tracking
		}
	}

	if version >= currentSchemaVersion {
		return nil
	}

	if version < 1 {
		if err := d.migrateV1(); err != nil {
			return err
		}
	}
	if version < 2 {
		if err := d.migrateV2(); err != nil {
			return err
		}
	}
	if version < 3 {
		if err := d.migrateV3(); err != nil {
			return err
		}
	}
	if version < 4 {
		if err := d.migrateV4(); err != nil {
			return err
		}
	}
	if version < 5 {
		if err := d.migrateV5(); err != nil {
			return err
		}
	}
	if version < 6 {
		if err := d.migrateV6(); err != nil {
			return err
		}
	}

	// Upsert version
	_, err = d.db.Exec(`DELETE FROM schema_version`)
	if err != nil {
		return err
	}
	_, err = d.db.Exec(`INSERT INTO schema_version (version) VALUES (?)`, currentSchemaVersion)
	return err
}

func (d *DB) migrateV1() error {
	_, err := d.db.Exec(`
		CREATE TABLE IF NOT EXISTS files (
			path TEXT PRIMARY KEY,
			hash TEXT NOT NULL,
			mtime INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS symbols (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			type TEXT NOT NULL,
			file TEXT NOT NULL,
			start_line INTEGER NOT NULL,
			end_line INTEGER NOT NULL,
			start_byte INTEGER NOT NULL,
			end_byte INTEGER NOT NULL,
			parent_id INTEGER DEFAULT NULL,
			FOREIGN KEY (file) REFERENCES files(path)
		);
		CREATE INDEX IF NOT EXISTS idx_symbols_name ON symbols(name);
		CREATE INDEX IF NOT EXISTS idx_symbols_file ON symbols(file);
		CREATE INDEX IF NOT EXISTS idx_symbols_type ON symbols(type);
	`)
	return err
}

func (d *DB) migrateV2() error {
	_, err := d.db.Exec(`
		CREATE TABLE IF NOT EXISTS imports (
			file TEXT NOT NULL,
			import_path TEXT NOT NULL,
			alias TEXT DEFAULT ''
		);
		CREATE INDEX IF NOT EXISTS idx_imports_file ON imports(file);
		CREATE INDEX IF NOT EXISTS idx_imports_path ON imports(import_path);

		CREATE TABLE IF NOT EXISTS refs (
			file TEXT NOT NULL,
			from_symbol_id INTEGER NOT NULL,
			to_name TEXT NOT NULL,
			line INTEGER NOT NULL,
			kind TEXT DEFAULT 'identifier'
		);
		CREATE INDEX IF NOT EXISTS idx_refs_to_name ON refs(to_name);
		CREATE INDEX IF NOT EXISTS idx_refs_from_symbol ON refs(from_symbol_id);
	`)
	if err != nil {
		return err
	}
	// Force full re-index so imports and refs get populated
	_, err = d.db.Exec(`UPDATE files SET hash = ''`)
	return err
}

func (d *DB) migrateV3() error {
	// Add parent_id column — idempotent: ignore error if column already exists
	_, err := d.db.Exec(`ALTER TABLE symbols ADD COLUMN parent_id INTEGER DEFAULT NULL REFERENCES symbols(id)`)
	if err != nil {
		// Check if column already exists (SQLite returns "duplicate column name" error)
		if !strings.Contains(err.Error(), "duplicate column") {
			return err
		}
	}
	_, err = d.db.Exec(`CREATE INDEX IF NOT EXISTS idx_symbols_parent ON symbols(parent_id)`)
	if err != nil {
		return err
	}
	// Force full re-index so parent_id gets populated
	_, err = d.db.Exec(`UPDATE files SET hash = ''`)
	return err
}

func (d *DB) migrateV4() error {
	// Rebuild symbols table: drop unused 'summary' column, ensure clean schema.
	_, err := d.db.Exec(`
		CREATE TABLE IF NOT EXISTS symbols_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			type TEXT NOT NULL,
			file TEXT NOT NULL,
			start_line INTEGER NOT NULL,
			end_line INTEGER NOT NULL,
			start_byte INTEGER NOT NULL,
			end_byte INTEGER NOT NULL,
			parent_id INTEGER DEFAULT NULL REFERENCES symbols_new(id),
			FOREIGN KEY (file) REFERENCES files(path)
		);
		INSERT INTO symbols_new (id, name, type, file, start_line, end_line, start_byte, end_byte, parent_id)
			SELECT id, name, type, file, start_line, end_line, start_byte, end_byte, parent_id FROM symbols;
		DROP TABLE symbols;
		ALTER TABLE symbols_new RENAME TO symbols;
		CREATE INDEX IF NOT EXISTS idx_symbols_name ON symbols(name);
		CREATE INDEX IF NOT EXISTS idx_symbols_file ON symbols(file);
		CREATE INDEX IF NOT EXISTS idx_symbols_type ON symbols(type);
		CREATE INDEX IF NOT EXISTS idx_symbols_parent ON symbols(parent_id);
	`)
	return err
}

func (d *DB) migrateV5() error {
	_, err := d.db.Exec(`CREATE INDEX IF NOT EXISTS idx_refs_file ON refs(file)`)
	return err
}

func (d *DB) migrateV6() error {
	_, err := d.db.Exec(`CREATE TABLE IF NOT EXISTS index_meta (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`)
	return err
}

// GetMeta reads a value from the index_meta table.
func (d *DB) GetMeta(key string) (string, error) {
	var value string
	err := d.db.QueryRow("SELECT value FROM index_meta WHERE key = ?", key).Scan(&value)
	return value, err
}

// SetMeta writes a key-value pair to the index_meta table.
func (d *DB) SetMeta(key, value string) error {
	_, err := d.db.Exec(
		"INSERT OR REPLACE INTO index_meta (key, value) VALUES (?, ?)",
		key, value,
	)
	return err
}

// UpsertFile updates or inserts a file record.
func (d *DB) UpsertFile(ctx context.Context, path, hash string, mtime int64) error {
	_, err := d.writerExecer().ExecContext(ctx, `
		INSERT INTO files (path, hash, mtime) VALUES (?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET hash=excluded.hash, mtime=excluded.mtime
	`, path, hash, mtime)
	return err
}

// GetFileHash returns the stored hash for a file, or empty string if not indexed.
func (d *DB) GetFileHash(ctx context.Context, path string) (string, error) {
	var hash string
	err := d.db.QueryRowContext(ctx, "SELECT hash FROM files WHERE path = ?", path).Scan(&hash)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return hash, err
}

// ClearFileData removes all symbols, imports, and refs for a file.
func (d *DB) ClearFileData(ctx context.Context, file string) error {
	q := d.writerExecer()
	// Delete refs that reference symbols in this file
	_, err := q.ExecContext(ctx, `DELETE FROM refs WHERE file = ?`, file)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, "DELETE FROM imports WHERE file = ?", file)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, "DELETE FROM symbols WHERE file = ?", file)
	return err
}

// ClearSymbols removes all symbols for a file (legacy compat).
func (d *DB) ClearSymbols(ctx context.Context, file string) error {
	return d.ClearFileData(ctx, file)
}

// InsertSymbol adds a symbol to the index.
func (d *DB) InsertSymbol(ctx context.Context, s SymbolInfo) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO symbols (name, type, file, start_line, end_line, start_byte, end_byte)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, s.Name, s.Type, s.File, s.StartLine, s.EndLine, s.StartByte, s.EndByte)
	return err
}

// InsertSymbolReturnID adds a symbol and returns its rowid.
// parentID is optional — pass nil for top-level symbols.
func (d *DB) InsertSymbolReturnID(ctx context.Context, s SymbolInfo, parentID *int64) (int64, error) {
	res, err := d.db.ExecContext(ctx, `
		INSERT INTO symbols (name, type, file, start_line, end_line, start_byte, end_byte, parent_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, s.Name, s.Type, s.File, s.StartLine, s.EndLine, s.StartByte, s.EndByte, parentID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateSymbolParent sets the parent_id for a symbol.
func (d *DB) UpdateSymbolParent(ctx context.Context, symbolID, parentID int64) error {
	_, err := d.db.ExecContext(ctx, `UPDATE symbols SET parent_id = ? WHERE id = ?`, parentID, symbolID)
	return err
}

// InsertSymbolsBatch inserts all symbols for a file in a single transaction,
// resolving parent_id using ParentIndex (pre-order guarantees parents first).
// Returns a map of parse-time index → DB ID for ref extraction.
func (d *DB) InsertSymbolsBatch(ctx context.Context, symbols []SymbolInfo) (map[int]int64, error) {
	ids := make(map[int]int64, len(symbols))
	if len(symbols) == 0 {
		return ids, nil
	}

	q, commitFn, err := d.localTxOrBatch(ctx)
	if err != nil {
		return nil, err
	}

	stmt, err := q.PrepareContext(ctx, `
		INSERT INTO symbols (name, type, file, start_line, end_line, start_byte, end_byte, parent_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()

	for i, s := range symbols {
		var parentID *int64
		if s.ParentIndex >= 0 {
			pid := ids[s.ParentIndex]
			parentID = &pid
		}
		res, err := stmt.ExecContext(ctx, s.Name, s.Type, s.File, s.StartLine, s.EndLine, s.StartByte, s.EndByte, parentID)
		if err != nil {
			return nil, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, err
		}
		ids[i] = id
	}

	return ids, commitFn()
}

// ImportInfo represents an import statement extracted from a source file.
type ImportInfo struct {
	File       string // importing file path
	ImportPath string // raw import string
	Alias      string // "", ".", alias name, or "*"
}

// RefInfo represents a reference edge from one symbol to an identifier.
type RefInfo struct {
	FromSymbolID int64
	ToName       string
	Line         uint32
	Kind         string // "identifier", "type", "field", "call"
}

// InsertImports bulk-inserts import records for a file.
func (d *DB) InsertImports(ctx context.Context, imports []ImportInfo) error {
	if len(imports) == 0 {
		return nil
	}
	q, commitFn, err := d.localTxOrBatch(ctx)
	if err != nil {
		return err
	}

	stmt, err := q.PrepareContext(ctx, `INSERT INTO imports (file, import_path, alias) VALUES (?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, imp := range imports {
		if _, err := stmt.ExecContext(ctx, imp.File, imp.ImportPath, imp.Alias); err != nil {
			return err
		}
	}
	return commitFn()
}

// InsertRefs bulk-inserts reference edges for a file.
func (d *DB) InsertRefs(ctx context.Context, file string, refs []RefInfo) error {
	if len(refs) == 0 {
		return nil
	}
	q, commitFn, err := d.localTxOrBatch(ctx)
	if err != nil {
		return err
	}

	stmt, err := q.PrepareContext(ctx, `INSERT INTO refs (file, from_symbol_id, to_name, line, kind) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range refs {
		if _, err := stmt.ExecContext(ctx, file, r.FromSymbolID, r.ToName, r.Line, r.Kind); err != nil {
			return err
		}
	}
	return commitFn()
}

// HasRefs returns true if the refs table has any data.
func (d *DB) HasRefs(ctx context.Context) bool {
	var count int
	err := d.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM refs LIMIT 1").Scan(&count)
	return err == nil && count > 0
}

// FindSemanticReferences finds references to a symbol, filtered by import visibility.
// symbolFile is the file where the target symbol is defined.
func (d *DB) FindSemanticReferences(ctx context.Context, symbolName, symbolFile string) ([]SymbolInfo, error) {
	// Find all refs to this name, join with the containing symbol
	rows, err := d.db.QueryContext(ctx, `
		SELECT r.file, r.line, s.name, s.type, s.file, s.start_line, s.end_line, s.start_byte, s.end_byte
		FROM refs r
		JOIN symbols s ON s.id = r.from_symbol_id
		WHERE r.to_name = ?
	`, symbolName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type refRow struct {
		refFile   string
		refLine   int
		container SymbolInfo
	}
	var candidates []refRow
	for rows.Next() {
		var rr refRow
		if err := rows.Scan(&rr.refFile, &rr.refLine,
			&rr.container.Name, &rr.container.Type, &rr.container.File,
			&rr.container.StartLine, &rr.container.EndLine,
			&rr.container.StartByte, &rr.container.EndByte); err != nil {
			return nil, err
		}
		candidates = append(candidates, rr)
	}

	// Filter by import visibility
	var results []SymbolInfo
	seen := make(map[string]bool)
	importCache := make(map[string][]ImportInfo)

	for _, c := range candidates {
		// Same-file refs always included
		if c.refFile == symbolFile {
			key := c.container.File + ":" + c.container.Name
			if !seen[key] {
				seen[key] = true
				results = append(results, SymbolInfo{
					Type:      "reference",
					Name:      symbolName,
					File:      c.refFile,
					StartLine: uint32(c.refLine),
					EndLine:   uint32(c.refLine),
				})
			}
			continue
		}

		// Check if the referring file imports the symbol's file
		imports, ok := importCache[c.refFile]
		if !ok {
			imports = d.getImportsForFile(ctx, c.refFile)
			importCache[c.refFile] = imports
		}

		if importsReach(imports, symbolFile, c.refFile, d.root) {
			key := c.refFile + ":" + fmt.Sprintf("%d", c.refLine)
			if !seen[key] {
				seen[key] = true
				results = append(results, SymbolInfo{
					Type:      "reference",
					Name:      symbolName,
					File:      c.refFile,
					StartLine: uint32(c.refLine),
					EndLine:   uint32(c.refLine),
				})
			}
		}
	}

	return results, nil
}

// FindReferencingFiles returns the set of files that reference symbolName and
// pass import-visibility checks relative to symbolFile. Used by rename to
// restrict identifier scanning to semantically relevant files.
func (d *DB) FindReferencingFiles(ctx context.Context, symbolName, symbolFile string) ([]string, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT DISTINCT r.file FROM refs r WHERE r.to_name = ?`, symbolName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	importCache := make(map[string][]ImportInfo)
	var files []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			continue
		}
		// Same-file always included
		if f == symbolFile {
			files = append(files, f)
			continue
		}
		// Check import visibility
		imports, ok := importCache[f]
		if !ok {
			imports = d.getImportsForFile(ctx, f)
			importCache[f] = imports
		}
		if importsReach(imports, symbolFile, f, d.root) {
			files = append(files, f)
		}
	}
	return files, nil
}

// FindSemanticCallers finds symbols that call/reference the given symbol, filtered by imports.
func (d *DB) FindSemanticCallers(ctx context.Context, symbolName, symbolFile string) ([]SymbolInfo, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT DISTINCT s.name, s.type, s.file, s.start_line, s.end_line, s.start_byte, s.end_byte
		FROM refs r
		JOIN symbols s ON s.id = r.from_symbol_id
		WHERE r.to_name = ?
	`, symbolName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []SymbolInfo
	for rows.Next() {
		var s SymbolInfo
		if err := rows.Scan(&s.Name, &s.Type, &s.File, &s.StartLine, &s.EndLine, &s.StartByte, &s.EndByte); err != nil {
			return nil, err
		}
		candidates = append(candidates, s)
	}

	// Filter by import visibility
	var results []SymbolInfo
	seen := make(map[string]bool)
	importCache := make(map[string][]ImportInfo)

	for _, c := range candidates {
		key := c.File + ":" + c.Name
		if seen[key] {
			continue
		}

		if c.File == symbolFile {
			// Same-file: skip self
			if c.Name == symbolName {
				continue
			}
			seen[key] = true
			results = append(results, c)
			continue
		}

		imports, ok := importCache[c.File]
		if !ok {
			imports = d.getImportsForFile(ctx, c.File)
			importCache[c.File] = imports
		}

		if importsReach(imports, symbolFile, c.File, d.root) {
			seen[key] = true
			results = append(results, c)
		}
	}

	return results, nil
}

// FindSameFileCallers returns symbols in the same file that reference symbolName,
// including the symbol itself (unlike FindSemanticCallers which skips self-refs).
// This is used by rename to ensure all same-file references are captured.
func (d *DB) FindSameFileCallers(ctx context.Context, symbolName, symbolFile string) ([]SymbolInfo, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT DISTINCT s.name, s.type, s.file, s.start_line, s.end_line, s.start_byte, s.end_byte
		FROM refs r
		JOIN symbols s ON s.id = r.from_symbol_id
		WHERE r.to_name = ? AND r.file = ?
	`, symbolName, symbolFile)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SymbolInfo
	seen := make(map[string]bool)
	for rows.Next() {
		var s SymbolInfo
		if err := rows.Scan(&s.Name, &s.Type, &s.File, &s.StartLine, &s.EndLine, &s.StartByte, &s.EndByte); err != nil {
			return nil, err
		}
		key := s.File + ":" + s.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		results = append(results, s)
	}
	return results, nil
}

// FindSemanticDeps finds symbols that the given symbol depends on, filtered by imports.
func (d *DB) FindSemanticDeps(ctx context.Context, symbolID int64, symbolFile string) ([]SymbolInfo, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT DISTINCT r.to_name
		FROM refs r
		WHERE r.from_symbol_id = ?
	`, symbolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}

	if len(names) == 0 {
		return nil, nil
	}

	// Get imports for the symbol's file
	imports := d.getImportsForFile(ctx, symbolFile)

	var results []SymbolInfo
	seen := make(map[string]bool)

	for _, name := range names {
		// Look up symbols with this exact name
		syms, err := d.db.QueryContext(ctx, `
			SELECT name, type, file, start_line, end_line, start_byte, end_byte
			FROM symbols WHERE name = ?
		`, name)
		if err != nil {
			continue
		}

		for syms.Next() {
			var s SymbolInfo
			if err := syms.Scan(&s.Name, &s.Type, &s.File, &s.StartLine, &s.EndLine, &s.StartByte, &s.EndByte); err != nil {
				continue
			}
			key := s.File + ":" + s.Name
			if seen[key] {
				continue
			}
			// Same file or import-reachable
			if s.File == symbolFile || importsReach(imports, s.File, symbolFile, d.root) {
				seen[key] = true
				results = append(results, s)
			}
		}
		syms.Close()
	}

	return results, nil
}

// GetSymbolID returns the DB row ID for a symbol.
func (d *DB) GetSymbolID(ctx context.Context, file, name string) (int64, error) {
	var id int64
	err := d.db.QueryRowContext(ctx, `SELECT id FROM symbols WHERE file = ? AND name = ?`, file, name).Scan(&id)
	return id, err
}

func (d *DB) getImportsForFile(ctx context.Context, file string) []ImportInfo {
	rows, err := d.db.QueryContext(ctx, `SELECT file, import_path, alias FROM imports WHERE file = ?`, file)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var imports []ImportInfo
	for rows.Next() {
		var imp ImportInfo
		if err := rows.Scan(&imp.File, &imp.ImportPath, &imp.Alias); err != nil {
			continue
		}
		imports = append(imports, imp)
	}
	return imports
}

// SearchSymbols finds symbols matching a name pattern.
func (d *DB) SearchSymbols(ctx context.Context, pattern string, limit ...int) ([]SymbolInfo, error) {
	if pattern == "" {
		return nil, fmt.Errorf("search requires a non-empty pattern")
	}
	query := `SELECT name, type, file, start_line, end_line, start_byte, end_byte
		FROM symbols WHERE name LIKE ?
		ORDER BY name`
	args := []any{"%" + pattern + "%"}
	if len(limit) > 0 && limit[0] > 0 {
		query += " LIMIT ?"
		args = append(args, limit[0])
	}
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SymbolInfo
	for rows.Next() {
		var s SymbolInfo
		if err := rows.Scan(&s.Name, &s.Type, &s.File, &s.StartLine, &s.EndLine, &s.StartByte, &s.EndByte); err != nil {
			return nil, err
		}
		results = append(results, s)
	}
	return results, nil
}

// GetSymbol returns a specific symbol by name and file.
func (d *DB) GetSymbol(ctx context.Context, file, name string) (*SymbolInfo, error) {
	var s SymbolInfo
	err := d.db.QueryRowContext(ctx, `
		SELECT name, type, file, start_line, end_line, start_byte, end_byte
		FROM symbols WHERE file = ? AND name = ?
	`, file, name).Scan(&s.Name, &s.Type, &s.File, &s.StartLine, &s.EndLine, &s.StartByte, &s.EndByte)
	if err == sql.ErrNoRows {
		return nil, d.symbolNotFoundError(ctx, name, file)
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// ResolveSymbol finds a symbol by name across all files. Returns an error if
// not found or if ambiguous (multiple files contain a symbol with that name).
func (d *DB) ResolveSymbol(ctx context.Context, name string) (*SymbolInfo, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT name, type, file, start_line, end_line, start_byte, end_byte
		FROM symbols WHERE name = ?
		ORDER BY file
	`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SymbolInfo
	for rows.Next() {
		var s SymbolInfo
		if err := rows.Scan(&s.Name, &s.Type, &s.File, &s.StartLine, &s.EndLine, &s.StartByte, &s.EndByte); err != nil {
			return nil, err
		}
		results = append(results, s)
	}
	if len(results) == 0 {
		// Try case-insensitive fallback
		ciRows, err := d.db.QueryContext(ctx, `
			SELECT name, type, file, start_line, end_line, start_byte, end_byte
			FROM symbols WHERE name = ? COLLATE NOCASE
			ORDER BY file
		`, name)
		if err == nil {
			defer ciRows.Close()
			for ciRows.Next() {
				var s SymbolInfo
				if err := ciRows.Scan(&s.Name, &s.Type, &s.File, &s.StartLine, &s.EndLine, &s.StartByte, &s.EndByte); err != nil {
					break
				}
				results = append(results, s)
			}
		}
		if len(results) == 0 {
			return nil, d.symbolNotFoundError(ctx, name, "")
		}
		if len(results) == 1 {
			return &results[0], nil
		}
		// Multiple case-insensitive matches — still ambiguous
		return nil, &AmbiguousSymbolError{Name: name, Root: d.root, Candidates: results}
	}
	if len(results) > 1 {
		return nil, &AmbiguousSymbolError{Name: name, Root: d.root, Candidates: results}
	}
	return &results[0], nil
}

// GetSymbolsByFile returns all symbols in a file.
func (d *DB) GetSymbolsByFile(ctx context.Context, file string) ([]SymbolInfo, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT name, type, file, start_line, end_line, start_byte, end_byte
		FROM symbols WHERE file = ?
		ORDER BY start_line
	`, file)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SymbolInfo
	for rows.Next() {
		var s SymbolInfo
		if err := rows.Scan(&s.Name, &s.Type, &s.File, &s.StartLine, &s.EndLine, &s.StartByte, &s.EndByte); err != nil {
			return nil, err
		}
		results = append(results, s)
	}
	return results, nil
}

// AllSymbols returns all indexed symbols.
func (d *DB) AllSymbols(ctx context.Context) ([]SymbolInfo, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT name, type, file, start_line, end_line, start_byte, end_byte
		FROM symbols ORDER BY file, start_line
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SymbolInfo
	for rows.Next() {
		var s SymbolInfo
		if err := rows.Scan(&s.Name, &s.Type, &s.File, &s.StartLine, &s.EndLine, &s.StartByte, &s.EndByte); err != nil {
			return nil, err
		}
		results = append(results, s)
	}
	return results, nil
}

// GetAllFileHashes loads all (path → content hash) pairs in a single query.
func (d *DB) GetAllFileHashes(ctx context.Context) (map[string]string, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT path, hash FROM files")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var path, hash string
		if err := rows.Scan(&path, &hash); err != nil {
			return nil, err
		}
		result[path] = hash
	}
	return result, rows.Err()
}

// GetAllFileMeta loads all (path → mtime) pairs for stale detection.
func (d *DB) GetAllFileMeta(ctx context.Context) (map[string]int64, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT path, mtime FROM files")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]int64)
	for rows.Next() {
		var path string
		var mtime int64
		if err := rows.Scan(&path, &mtime); err != nil {
			return nil, err
		}
		result[path] = mtime
	}
	return result, rows.Err()
}

// FilteredSymbols returns symbols with optional filters pushed to SQL.
// dir is an absolute path prefix, symbolType is exact match, namePattern
// is a case-insensitive substring (use empty string to skip a filter).
func (d *DB) FilteredSymbols(ctx context.Context, dir, symbolType, namePattern string) ([]SymbolInfo, error) {
	query := "SELECT name, type, file, start_line, end_line, start_byte, end_byte FROM symbols"
	var conditions []string
	var args []any

	if dir != "" {
		// file paths are absolute; match prefix
		conditions = append(conditions, "file GLOB ?")
		args = append(args, dir+"/*")
	}
	if symbolType != "" {
		conditions = append(conditions, "type = ?")
		args = append(args, symbolType)
	}
	if namePattern != "" {
		// Escape LIKE wildcards so %, _, and \ are treated as literals.
		esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(namePattern)
		conditions = append(conditions, "name LIKE ? ESCAPE '\\'")
		args = append(args, "%"+esc+"%")
	}

	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY file, start_line"

	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SymbolInfo
	for rows.Next() {
		var s SymbolInfo
		if err := rows.Scan(&s.Name, &s.Type, &s.File, &s.StartLine, &s.EndLine, &s.StartByte, &s.EndByte); err != nil {
			return nil, err
		}
		results = append(results, s)
	}
	return results, rows.Err()
}

// Stats returns index statistics.
func (d *DB) Stats(ctx context.Context) (files int, symbols int, err error) {
	err = d.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM files").Scan(&files)
	if err != nil {
		return
	}
	err = d.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM symbols").Scan(&symbols)
	return
}

// ResolvePath converts a repo-relative or absolute path to an absolute path
// under the repository root.
func (d *DB) ResolvePath(path string) (string, error) {
	return ResolvePath(d.root, path)
}

// Prune removes indexed files that are outside the repo root or no longer exist.
func (d *DB) Prune(ctx context.Context) error {
	rows, err := d.db.QueryContext(ctx, "SELECT path FROM files")
	if err != nil {
		return err
	}
	defer rows.Close()

	var stale []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return err
		}
		if !IsWithinRoot(d.root, path) {
			stale = append(stale, path)
			continue
		}
		if _, err := os.Stat(path); err != nil && os.IsNotExist(err) {
			stale = append(stale, path)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(stale) == 0 {
		return nil
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, path := range stale {
		if _, err := tx.ExecContext(ctx, "DELETE FROM refs WHERE file = ?", path); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM imports WHERE file = ?", path); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM symbols WHERE file = ?", path); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM files WHERE path = ?", path); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// symbolNotFoundError builds a helpful error message with suggestions.
func (d *DB) symbolNotFoundError(ctx context.Context, name, file string) error {
	var msg string
	if file != "" {
		msg = fmt.Sprintf("symbol %q not found in %s", name, file)
	} else {
		msg = fmt.Sprintf("symbol %q not found", name)
	}

	var candidates []SymbolInfo

	if file != "" {
		// File-scoped: get all symbols from this file and rank by similarity
		candidates, _ = d.GetSymbolsByFile(ctx, file)
	} else {
		// Global: search by substring first
		candidates, _ = d.SearchSymbols(ctx, name)
	}

	// If substring match found nothing, try splitting camelCase/PascalCase
	// into parts and searching for each part
	if len(candidates) == 0 && file == "" {
		parts := splitCamelCase(name)
		if len(parts) > 1 {
			for _, part := range parts {
				if len(part) < 3 {
					continue
				}
				partResults, _ := d.SearchSymbols(ctx, part, 10)
				candidates = append(candidates, partResults...)
			}
		}
	}

	if len(candidates) == 0 {
		return fmt.Errorf("%s", msg)
	}

	// Rank candidates by similarity to the query name
	type scored struct {
		name  string
		score int
	}
	seen := make(map[string]bool)
	var ranked []scored
	nameL := strings.ToLower(name)
	for _, s := range candidates {
		if seen[s.Name] || s.Name == name || len(s.Name) < 3 {
			continue
		}
		seen[s.Name] = true
		score := symbolSimilarity(nameL, strings.ToLower(s.Name))
		if score > 0 {
			ranked = append(ranked, scored{s.Name, score})
		}
	}

	// Sort by score descending
	for i := 0; i < len(ranked); i++ {
		for j := i + 1; j < len(ranked); j++ {
			if ranked[j].score > ranked[i].score {
				ranked[i], ranked[j] = ranked[j], ranked[i]
			}
		}
	}

	var names []string
	for i, r := range ranked {
		if i >= 5 {
			break
		}
		names = append(names, r.name)
	}

	if len(names) == 0 {
		return fmt.Errorf("%s", msg)
	}

	return fmt.Errorf("%s. Did you mean: %s", msg, strings.Join(names, ", "))
}

// splitCamelCase splits "ApplyEdit" into ["Apply", "Edit"].
func splitCamelCase(s string) []string {
	var parts []string
	start := 0
	for i := 1; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' && s[i-1] >= 'a' && s[i-1] <= 'z' {
			parts = append(parts, s[start:i])
			start = i
		}
	}
	parts = append(parts, s[start:])
	return parts
}

// symbolSimilarity returns a score for how similar two lowercase symbol names are.
// Higher is better. Returns 0 for no meaningful similarity.
func symbolSimilarity(query, candidate string) int {
	score := 0
	// Exact substring match (either direction)
	if strings.Contains(candidate, query) {
		score += 10
	} else if strings.Contains(query, candidate) {
		score += 8
	}
	// Shared prefix
	prefixLen := 0
	for i := 0; i < len(query) && i < len(candidate); i++ {
		if query[i] != candidate[i] {
			break
		}
		prefixLen++
	}
	if prefixLen >= 3 {
		score += prefixLen
	}
	// Shared suffix
	suffixLen := 0
	for i := 0; i < len(query) && i < len(candidate); i++ {
		if query[len(query)-1-i] != candidate[len(candidate)-1-i] {
			break
		}
		suffixLen++
	}
	if suffixLen >= 3 {
		score += suffixLen
	}
	return score
}

// AmbiguousSymbolError is returned when a symbol name resolves to multiple definitions.
// It carries structured candidate information so callers can present choices.
type AmbiguousSymbolError struct {
	Name       string
	Root       string // repo root for computing relative paths
	Candidates []SymbolInfo
}

func (e *AmbiguousSymbolError) Error() string {
	var parts []string
	for _, c := range e.Candidates {
		rel := c.File
		if e.Root != "" && strings.HasPrefix(rel, e.Root+"/") {
			rel = rel[len(e.Root)+1:]
		}
		parts = append(parts, fmt.Sprintf("%s:%d (%s)", rel, c.StartLine, c.Type))
	}
	return fmt.Sprintf("symbol %q is ambiguous (%d definitions): %s — use [file] <symbol> to disambiguate",
		e.Name, len(e.Candidates), strings.Join(parts, ", "))
}

// GetChildSymbols returns symbols whose parent_id matches the given symbol's DB id.
func (d *DB) GetChildSymbols(ctx context.Context, parentID int64) ([]SymbolInfo, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT name, type, file, start_line, end_line, start_byte, end_byte
		FROM symbols WHERE parent_id = ?
		ORDER BY start_line
	`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SymbolInfo
	for rows.Next() {
		var s SymbolInfo
		if err := rows.Scan(&s.Name, &s.Type, &s.File, &s.StartLine, &s.EndLine, &s.StartByte, &s.EndByte); err != nil {
			return nil, err
		}
		results = append(results, s)
	}
	return results, nil
}

// GetContainerAt returns the innermost container symbol that spans the given line.
func (d *DB) GetContainerAt(ctx context.Context, file string, line int) (*SymbolInfo, error) {
	// Find the innermost symbol (smallest span) that contains the given line.
	// Containers are types like class, struct, impl, interface, module.
	var s SymbolInfo
	err := d.db.QueryRowContext(ctx, `
		SELECT name, type, file, start_line, end_line, start_byte, end_byte
		FROM symbols
		WHERE file = ? AND start_line <= ? AND end_line >= ?
		  AND type IN ('class', 'struct', 'impl', 'interface', 'module', 'enum')
		ORDER BY (end_line - start_line) ASC
		LIMIT 1
	`, file, line, line).Scan(&s.Name, &s.Type, &s.File, &s.StartLine, &s.EndLine, &s.StartByte, &s.EndByte)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no container symbol at %s:%d", file, line)
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// Root returns the repository root.
func (d *DB) Root() string {
	return d.root
}

// EdrDir returns the .edr directory path (root/.edr).
func (d *DB) EdrDir() string {
	return filepath.Join(d.root, ".edr")
}
