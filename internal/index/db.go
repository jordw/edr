package index

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// DB is the persistent index database.
type DB struct {
	db   *sql.DB
	root string
}

// OpenDB opens or creates the index database in the .edr directory.
func OpenDB(repoRoot string) (*DB, error) {
	edrDir := filepath.Join(repoRoot, ".edr")
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

	d := &DB{db: sqlDB, root: repoRoot}
	if err := d.migrate(); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return d, nil
}

func (d *DB) Close() error {
	return d.db.Close()
}

func (d *DB) migrate() error {
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

// ClearSymbols removes all symbols for a file.
func (d *DB) ClearSymbols(ctx context.Context, file string) error {
	_, err := d.db.ExecContext(ctx, "DELETE FROM symbols WHERE file = ?", file)
	return err
}

// InsertSymbol adds a symbol to the index.
func (d *DB) InsertSymbol(ctx context.Context, s SymbolInfo) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO symbols (name, type, file, start_line, end_line, start_byte, end_byte)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, s.Name, s.Type, s.File, s.StartLine, s.EndLine, s.StartByte, s.EndByte)
	return err
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
		return nil, fmt.Errorf("symbol %q not found in %s", name, file)
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
		return nil, fmt.Errorf("symbol %q not found", name)
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

// Root returns the repository root.
func (d *DB) Root() string {
	return d.root
}
