// Package lorgdb provides a lightweight SQLite wrapper that replaces PocketBase's
// DAO layer. It opens the same data.db file PocketBase used, so no data migration
// is required.
package lorgdb

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

// LorgDB wraps a *sql.DB with a mutex and convenience methods.
// All exported methods are goroutine-safe.
type LorgDB struct {
	mu       sync.RWMutex
	db       *sql.DB
	dbPath   string
	colCache sync.Map // table name → map[string]bool (known columns)
}

// tableColumns returns the set of column names for a table, using a cache.
func (d *LorgDB) tableColumns(table string) map[string]bool {
	if cached, ok := d.colCache.Load(table); ok {
		return cached.(map[string]bool)
	}
	cols := make(map[string]bool)
	rows, err := d.db.Query("PRAGMA table_info(\"" + table + "\")")
	if err != nil {
		return cols
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt *string
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			continue
		}
		cols[name] = true
	}
	if len(cols) > 0 {
		d.colCache.Store(table, cols)
	}
	return cols
}

// Open opens (or creates) the SQLite database at dbPath and applies pragmas.
func Open(dbPath string) (*LorgDB, error) {
	// Ensure parent directory exists.
	if dir := dbPath[:max(0, len(dbPath)-len("/data.db"))]; dir != "" {
		_ = os.MkdirAll(dir, 0755)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("lorgdb: open %s: %w", dbPath, err)
	}

	// Pragmas matching the ProjectDB pattern.
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("lorgdb: %s: %w", pragma, err)
		}
	}

	log.Printf("[LorgDB] Opened database: %s", dbPath)
	return &LorgDB{db: db, dbPath: dbPath}, nil
}

// Close closes the underlying database connection.
func (d *LorgDB) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.db != nil {
		return d.db.Close()
	}
	return nil
}

// Path returns the filesystem path of the open database.
func (d *LorgDB) Path() string { return d.dbPath }

// DB returns the underlying *sql.DB for advanced use cases (e.g. traffic_list
// raw queries). Callers must not close it.
func (d *LorgDB) DB() *sql.DB { return d.db }

// Exec executes a statement that returns no rows.
func (d *LorgDB) Exec(query string, args ...any) (sql.Result, error) {
	return d.db.Exec(query, args...)
}

// Query executes a query that returns rows.
func (d *LorgDB) Query(query string, args ...any) (*sql.Rows, error) {
	return d.db.Query(query, args...)
}

// QueryRow executes a query that returns at most one row.
func (d *LorgDB) QueryRow(query string, args ...any) *sql.Row {
	return d.db.QueryRow(query, args...)
}

// LorgTx wraps a *sql.Tx and exposes the same SaveRecord / Exec helpers as
// LorgDB so callers can use the same patterns inside transactions.
type LorgTx struct {
	tx *sql.Tx
	db *LorgDB // parent, for column cache access
}

// SaveRecord inserts or updates a record within the transaction.
func (t *LorgTx) SaveRecord(r *Record) error {
	if r.IsNew {
		return t.insertRecord(r)
	}
	return t.updateRecord(r)
}

func (t *LorgTx) insertRecord(r *Record) error {
	cols, placeholders, vals := buildInsertArgs(r, t.db.tableColumns(r.TableName))
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		r.TableName,
		strings.Join(cols, ", "),
		strings.Join(placeholders, ", "),
	)
	_, err := t.tx.Exec(query, vals...)
	if err != nil {
		return fmt.Errorf("lorgdb tx: insert into %s: %w", r.TableName, err)
	}
	r.IsNew = false
	return nil
}

func (t *LorgTx) updateRecord(r *Record) error {
	sets, vals := buildUpdateArgs(r, t.db.tableColumns(r.TableName))
	vals = append(vals, r.Id)
	query := fmt.Sprintf("UPDATE %s SET %s WHERE id = ?",
		r.TableName,
		strings.Join(sets, ", "),
	)
	_, err := t.tx.Exec(query, vals...)
	if err != nil {
		return fmt.Errorf("lorgdb tx: update %s id=%s: %w", r.TableName, r.Id, err)
	}
	return nil
}

// Exec executes a statement within the transaction.
func (t *LorgTx) Exec(query string, args ...any) (sql.Result, error) {
	return t.tx.Exec(query, args...)
}

// RunInTransaction executes fn inside a database transaction. If fn returns
// an error the transaction is rolled back; otherwise it is committed.
func (d *LorgDB) RunInTransaction(fn func(tx *LorgTx) error) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("lorgdb: begin tx: %w", err)
	}

	ltx := &LorgTx{tx: tx, db: d}
	if err := fn(ltx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
