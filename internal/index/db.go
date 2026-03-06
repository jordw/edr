package index

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// DB is the persistent index database.
type DB struct {
	db   *sql.DB
	root string
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

	// Enable WAL mode for better concurrent access
	if _, err := sqlDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
		sqlDB.Close()
		return nil, err
	}

	d := &DB{db: sqlDB, root: root}
	if err := d.migrate(); err != nil {
		sqlDB.Close()
		return nil, err
	}
	if err := d.Prune(context.Background()); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return d, nil
}

func (d *DB) Close() error {
	return d.db.Close()
}

const currentSchemaVersion = 2

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
			summary TEXT DEFAULT '',
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

// UpsertFile updates or inserts a file record.
func (d *DB) UpsertFile(ctx context.Context, path, hash string, mtime int64) error {
	_, err := d.db.ExecContext(ctx, `
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
	// Delete refs that reference symbols in this file
	_, err := d.db.ExecContext(ctx, `DELETE FROM refs WHERE file = ?`, file)
	if err != nil {
		return err
	}
	_, err = d.db.ExecContext(ctx, "DELETE FROM imports WHERE file = ?", file)
	if err != nil {
		return err
	}
	_, err = d.db.ExecContext(ctx, "DELETE FROM symbols WHERE file = ?", file)
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
func (d *DB) InsertSymbolReturnID(ctx context.Context, s SymbolInfo) (int64, error) {
	res, err := d.db.ExecContext(ctx, `
		INSERT INTO symbols (name, type, file, start_line, end_line, start_byte, end_byte)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, s.Name, s.Type, s.File, s.StartLine, s.EndLine, s.StartByte, s.EndByte)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
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
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO imports (file, import_path, alias) VALUES (?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, imp := range imports {
		if _, err := stmt.ExecContext(ctx, imp.File, imp.ImportPath, imp.Alias); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// InsertRefs bulk-inserts reference edges for a file.
func (d *DB) InsertRefs(ctx context.Context, file string, refs []RefInfo) error {
	if len(refs) == 0 {
		return nil
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO refs (file, from_symbol_id, to_name, line, kind) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range refs {
		if _, err := stmt.ExecContext(ctx, file, r.FromSymbolID, r.ToName, r.Line, r.Kind); err != nil {
			return err
		}
	}
	return tx.Commit()
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
func (d *DB) SearchSymbols(ctx context.Context, pattern string) ([]SymbolInfo, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT name, type, file, start_line, end_line, start_byte, end_byte
		FROM symbols WHERE name LIKE ?
		ORDER BY name
	`, "%"+pattern+"%")
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
		return nil, d.symbolNotFoundError(ctx, name, "")
	}
	if len(results) > 1 {
		files := make([]string, len(results))
		for i, r := range results {
			files[i] = r.File
		}
		return nil, fmt.Errorf("symbol %q is ambiguous, found in: %v — specify file", name, files)
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

	// Search for similar names
	suggestions, err := d.SearchSymbols(ctx, name)
	if err != nil || len(suggestions) == 0 {
		return fmt.Errorf("%s", msg)
	}

	// Deduplicate and limit to 5
	seen := make(map[string]bool)
	var names []string
	for _, s := range suggestions {
		if !seen[s.Name] {
			seen[s.Name] = true
			names = append(names, s.Name)
			if len(names) >= 5 {
				break
			}
		}
	}

	if len(names) == 0 {
		return fmt.Errorf("%s", msg)
	}

	return fmt.Errorf("%s. Did you mean: %s", msg, strings.Join(names, ", "))
}

// Root returns the repository root.
func (d *DB) Root() string {
	return d.root
}
