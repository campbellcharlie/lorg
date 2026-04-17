package lorgdb

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/campbellcharlie/lorg/internal/utils"
)

// Record is a thin replacement for *models.Record. It stores column values in
// a flat map, just like PocketBase records are stored as SQLite rows.
type Record struct {
	Id        string
	TableName string
	Data      map[string]any // column name → value (excluding id, created, updated)
	IsNew     bool
	Created   string
	Updated   string
}

// NewRecord creates a blank record for the given table, ready for Insert.
func NewRecord(tableName string) *Record {
	now := time.Now().UTC().Format("2006-01-02 15:04:05.000Z")
	return &Record{
		Id:        utils.RandomString(15),
		TableName: tableName,
		Data:      make(map[string]any),
		IsNew:     true,
		Created:   now,
		Updated:   now,
	}
}

// Get returns the value for key, or nil if not set.
func (r *Record) Get(key string) any {
	if key == "id" {
		return r.Id
	}
	return r.Data[key]
}

// Set sets a column value.
func (r *Record) Set(key string, val any) {
	if key == "id" {
		r.Id = fmt.Sprint(val)
		return
	}
	r.Data[key] = val
}

// GetString returns the value as a string. Returns "" if missing or wrong type.
func (r *Record) GetString(key string) string {
	if key == "id" {
		return r.Id
	}
	v, ok := r.Data[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

// GetBool returns the value as a bool.
func (r *Record) GetBool(key string) bool {
	v := r.Data[key]
	if v == nil {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case int64:
		return t != 0
	case float64:
		return t != 0
	case string:
		return t == "true" || t == "1"
	default:
		return false
	}
}

// GetInt returns the value as an int.
func (r *Record) GetInt(key string) int {
	v := r.Data[key]
	if v == nil {
		return 0
	}
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	case string:
		var n int
		fmt.Sscanf(t, "%d", &n)
		return n
	default:
		return 0
	}
}

// GetFloat returns the value as a float64.
func (r *Record) GetFloat(key string) float64 {
	v := r.Data[key]
	if v == nil {
		return 0
	}
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case json.Number:
		n, _ := t.Float64()
		return n
	default:
		return 0
	}
}

// Load bulk-sets all fields from a map, used for building records from
// parsed request/response data (mirrors models.Record.Load).
func (r *Record) Load(data map[string]any) {
	for k, v := range data {
		r.Set(k, v)
	}
}

// MarkAsNotNew flips IsNew to false, indicating the record already exists in
// the database and should be UPDATEd on the next save.
func (r *Record) MarkAsNotNew() {
	r.IsNew = false
}
