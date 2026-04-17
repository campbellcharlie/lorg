package lorgdb

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// SaveRecord inserts or updates a record. It uses r.IsNew to decide.
func (d *LorgDB) SaveRecord(r *Record) error {
	if r.IsNew {
		return d.insertRecord(r)
	}
	return d.updateRecord(r)
}

// insertRecord performs an INSERT for the record.
func (d *LorgDB) insertRecord(r *Record) error {
	cols, placeholders, vals := buildInsertArgs(r, d.tableColumns(r.TableName))
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		r.TableName,
		strings.Join(cols, ", "),
		strings.Join(placeholders, ", "),
	)
	_, err := d.db.Exec(query, vals...)
	if err != nil {
		return fmt.Errorf("lorgdb: insert into %s: %w", r.TableName, err)
	}
	r.IsNew = false
	return nil
}

// updateRecord performs an UPDATE for the record, setting all Data fields.
func (d *LorgDB) updateRecord(r *Record) error {
	sets, vals := buildUpdateArgs(r, d.tableColumns(r.TableName))
	vals = append(vals, r.Id)
	query := fmt.Sprintf("UPDATE %s SET %s WHERE id = ?",
		r.TableName,
		strings.Join(sets, ", "),
	)
	_, err := d.db.Exec(query, vals...)
	if err != nil {
		return fmt.Errorf("lorgdb: update %s id=%s: %w", r.TableName, r.Id, err)
	}
	return nil
}

// InsertMap is a fast-path INSERT for the proxy hot loop, avoiding Record
// allocation overhead.
func (d *LorgDB) InsertMap(tableName string, data map[string]any) error {
	cols := make([]string, 0, len(data))
	placeholders := make([]string, 0, len(data))
	vals := make([]any, 0, len(data))

	keys := sortedKeys(data)
	for _, k := range keys {
		cols = append(cols, quoteCol(k))
		placeholders = append(placeholders, "?")
		vals = append(vals, normalizeValue(data[k]))
	}

	query := fmt.Sprintf("INSERT INTO \"%s\" (%s) VALUES (%s)",
		tableName,
		strings.Join(cols, ", "),
		strings.Join(placeholders, ", "),
	)

	_, err := d.db.Exec(query, vals...)
	if err != nil {
		return fmt.Errorf("lorgdb: insertMap %s: %w", tableName, err)
	}
	return nil
}

// FindRecordById fetches a single record by primary key.
func (d *LorgDB) FindRecordById(tableName, id string) (*Record, error) {
	return d.findOne(tableName, "id = ?", id)
}

// FindFirstRecord fetches the first record matching the WHERE clause.
func (d *LorgDB) FindFirstRecord(tableName, where string, args ...any) (*Record, error) {
	return d.findOne(tableName, where, args...)
}

// FindRecords returns all records matching the WHERE clause, ordered by rowid.
func (d *LorgDB) FindRecords(tableName, where string, args ...any) ([]*Record, error) {
	query := fmt.Sprintf("SELECT * FROM %s WHERE %s ORDER BY rowid", tableName, where)
	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("lorgdb: find in %s: %w", tableName, err)
	}
	defer rows.Close()
	return scanRecords(tableName, rows)
}

// FindRecordsSorted returns records matching the WHERE clause with custom ORDER BY.
func (d *LorgDB) FindRecordsSorted(tableName, where, orderBy string, limit, offset int, args ...any) ([]*Record, error) {
	query := fmt.Sprintf("SELECT * FROM %s WHERE %s", tableName, where)
	if orderBy != "" {
		query += " ORDER BY " + orderBy
	}
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	if offset > 0 {
		query += fmt.Sprintf(" OFFSET %d", offset)
	}
	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("lorgdb: find in %s: %w", tableName, err)
	}
	defer rows.Close()
	return scanRecords(tableName, rows)
}

// DeleteRecord deletes a single record by ID.
func (d *LorgDB) DeleteRecord(tableName, id string) error {
	_, err := d.db.Exec(fmt.Sprintf("DELETE FROM %s WHERE id = ?", tableName), id)
	if err != nil {
		return fmt.Errorf("lorgdb: delete from %s id=%s: %w", tableName, id, err)
	}
	return nil
}

// DeleteWhere deletes all records matching the WHERE clause.
func (d *LorgDB) DeleteWhere(tableName, where string, args ...any) error {
	_, err := d.db.Exec(fmt.Sprintf("DELETE FROM %s WHERE %s", tableName, where), args...)
	if err != nil {
		return fmt.Errorf("lorgdb: delete from %s: %w", tableName, err)
	}
	return nil
}

// TableExists returns true if the named table exists in the database.
func (d *LorgDB) TableExists(tableName string) (bool, error) {
	var name string
	err := d.db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name=?",
		tableName,
	).Scan(&name)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// Shared helpers (used by both LorgDB and LorgTx)
// ---------------------------------------------------------------------------

// buildInsertArgs prepares columns, placeholders, and values for an INSERT.
// It also stamps created/updated timestamps on the record.
// If knownCols is non-nil, only columns in that set are included (unknown fields are silently skipped).
func buildInsertArgs(r *Record, knownCols map[string]bool) (cols, placeholders []string, vals []any) {
	now := time.Now().UTC().Format("2006-01-02 15:04:05.000Z")
	if r.Created == "" {
		r.Created = now
	}
	r.Updated = now

	cols = []string{"id", "created", "updated"}
	placeholders = []string{"?", "?", "?"}
	vals = []any{r.Id, r.Created, r.Updated}

	keys := sortedKeys(r.Data)
	for _, k := range keys {
		if knownCols != nil && !knownCols[k] {
			continue // skip fields that don't exist as columns
		}
		cols = append(cols, quoteCol(k))
		placeholders = append(placeholders, "?")
		vals = append(vals, normalizeValue(r.Data[k]))
	}
	return
}

// buildUpdateArgs prepares SET clauses and values for an UPDATE.
// It stamps the updated timestamp. The caller must append r.Id for the WHERE.
// If knownCols is non-nil, only columns in that set are included.
func buildUpdateArgs(r *Record, knownCols map[string]bool) (sets []string, vals []any) {
	now := time.Now().UTC().Format("2006-01-02 15:04:05.000Z")
	r.Updated = now

	sets = []string{"updated = ?"}
	vals = []any{r.Updated}

	keys := sortedKeys(r.Data)
	for _, k := range keys {
		if knownCols != nil && !knownCols[k] {
			continue
		}
		sets = append(sets, quoteCol(k)+" = ?")
		vals = append(vals, normalizeValue(r.Data[k]))
	}
	return
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// findOne returns the first matching record or sql.ErrNoRows.
func (d *LorgDB) findOne(tableName, where string, args ...any) (*Record, error) {
	query := fmt.Sprintf("SELECT * FROM %s WHERE %s LIMIT 1", tableName, where)
	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("lorgdb: find in %s: %w", tableName, err)
	}
	defer rows.Close()

	records, err := scanRecords(tableName, rows)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, sql.ErrNoRows
	}
	return records[0], nil
}

// scanRecords turns *sql.Rows into a slice of Records.
func scanRecords(tableName string, rows *sql.Rows) ([]*Record, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("lorgdb: columns for %s: %w", tableName, err)
	}

	var records []*Record
	for rows.Next() {
		dest := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range dest {
			ptrs[i] = &dest[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("lorgdb: scan %s: %w", tableName, err)
		}

		r := &Record{
			TableName: tableName,
			Data:      make(map[string]any, len(cols)),
			IsNew:     false,
		}
		for i, col := range cols {
			val := dest[i]
			// Convert []byte to string for ergonomic access.
			if b, ok := val.([]byte); ok {
				val = string(b)
			}
			// Auto-parse JSON strings so callers get maps/slices like PocketBase.
			if s, ok := val.(string); ok && len(s) > 0 && (s[0] == '{' || s[0] == '[') {
				var parsed any
				if json.Unmarshal([]byte(s), &parsed) == nil {
					val = parsed
				}
			}
			switch col {
			case "id":
				r.Id = fmt.Sprint(val)
			case "created":
				r.Created = fmt.Sprint(val)
			case "updated":
				r.Updated = fmt.Sprint(val)
			default:
				r.Data[col] = val
			}
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

// quoteCol wraps a column name in double quotes to handle reserved words like "index".
func quoteCol(name string) string {
	return "\"" + name + "\""
}

// sortedKeys returns the keys of a map sorted alphabetically.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// normalizeValue converts Go values to SQLite-compatible representations.
// Maps, slices, and other complex types are JSON-encoded.
func normalizeValue(v any) any {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case string, int, int64, float64, bool:
		return t
	case []byte:
		return string(t)
	case json.Number:
		return string(t)
	default:
		// JSON-encode maps, slices, structs, etc.
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprint(t)
		}
		return string(b)
	}
}
