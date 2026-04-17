package lorgdb

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// testDB creates an in-memory-style temp DB for testing.
func testDB(t *testing.T) *LorgDB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RunMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenAndClose(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if db.Path() != dbPath {
		t.Errorf("Path() = %q, want %q", db.Path(), dbPath)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("DB file not created: %v", err)
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	db := testDB(t)
	// Running again should be a no-op.
	if err := db.RunMigrations(); err != nil {
		t.Fatal(err)
	}
}

func TestInsertAndFindById(t *testing.T) {
	db := testDB(t)

	r := NewRecord("_labels")
	r.Set("name", "test-label")
	r.Set("color", "red")
	r.Set("icon", "")
	r.Set("type", "custom")

	if err := db.SaveRecord(r); err != nil {
		t.Fatal(err)
	}

	if r.IsNew {
		t.Error("record should not be IsNew after insert")
	}

	got, err := db.FindRecordById("_labels", r.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got.GetString("name") != "test-label" {
		t.Errorf("name = %q, want %q", got.GetString("name"), "test-label")
	}
	if got.GetString("color") != "red" {
		t.Errorf("color = %q, want %q", got.GetString("color"), "red")
	}
}

func TestUpdateRecord(t *testing.T) {
	db := testDB(t)

	r := NewRecord("_labels")
	r.Set("name", "update-test")
	r.Set("color", "blue")
	r.Set("icon", "")
	r.Set("type", "mark")
	if err := db.SaveRecord(r); err != nil {
		t.Fatal(err)
	}

	r.Set("color", "green")
	if err := db.SaveRecord(r); err != nil {
		t.Fatal(err)
	}

	got, err := db.FindRecordById("_labels", r.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got.GetString("color") != "green" {
		t.Errorf("color after update = %q, want %q", got.GetString("color"), "green")
	}
}

func TestFindRecords(t *testing.T) {
	db := testDB(t)

	for _, name := range []string{"aaa", "bbb", "ccc"} {
		r := NewRecord("_labels")
		r.Set("name", name)
		r.Set("color", "red")
		r.Set("icon", "")
		r.Set("type", "custom")
		if err := db.SaveRecord(r); err != nil {
			t.Fatal(err)
		}
	}

	records, err := db.FindRecords("_labels", "color = ?", "red")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Errorf("found %d records, want 3", len(records))
	}
}

func TestFindFirstRecord(t *testing.T) {
	db := testDB(t)

	r := NewRecord("_settings")
	r.Set("id", "TEST___________")
	r.Id = "TEST___________"
	r.Set("option", "Test Setting")
	r.Set("value", "42")
	if err := db.SaveRecord(r); err != nil {
		t.Fatal(err)
	}

	got, err := db.FindFirstRecord("_settings", "option = ?", "Test Setting")
	if err != nil {
		t.Fatal(err)
	}
	if got.GetString("value") != "42" {
		t.Errorf("value = %q, want %q", got.GetString("value"), "42")
	}
}

func TestDeleteRecord(t *testing.T) {
	db := testDB(t)

	r := NewRecord("_labels")
	r.Set("name", "to-delete")
	r.Set("color", "red")
	r.Set("icon", "")
	r.Set("type", "custom")
	if err := db.SaveRecord(r); err != nil {
		t.Fatal(err)
	}

	if err := db.DeleteRecord("_labels", r.Id); err != nil {
		t.Fatal(err)
	}

	_, err := db.FindRecordById("_labels", r.Id)
	if err == nil {
		t.Error("expected error after delete, got nil")
	}
}

func TestDeleteWhere(t *testing.T) {
	db := testDB(t)

	for i := 0; i < 3; i++ {
		r := NewRecord("_labels")
		r.Set("name", "del-"+string(rune('a'+i)))
		r.Set("color", "deletecolor")
		r.Set("icon", "")
		r.Set("type", "custom")
		if err := db.SaveRecord(r); err != nil {
			t.Fatal(err)
		}
	}

	if err := db.DeleteWhere("_labels", "color = ?", "deletecolor"); err != nil {
		t.Fatal(err)
	}

	records, err := db.FindRecords("_labels", "color = ?", "deletecolor")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Errorf("found %d records after DeleteWhere, want 0", len(records))
	}
}

func TestInsertMap(t *testing.T) {
	db := testDB(t)

	err := db.InsertMap("_labels", map[string]any{
		"id":      "testmap12345678",
		"created": "2025-01-01 00:00:00.000Z",
		"updated": "2025-01-01 00:00:00.000Z",
		"name":    "map-label",
		"color":   "purple",
		"icon":    "",
		"type":    "custom",
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := db.FindRecordById("_labels", "testmap12345678")
	if err != nil {
		t.Fatal(err)
	}
	if got.GetString("name") != "map-label" {
		t.Errorf("name = %q, want %q", got.GetString("name"), "map-label")
	}
}

func TestRunInTransaction(t *testing.T) {
	db := testDB(t)

	err := db.RunInTransaction(func(tx *LorgTx) error {
		r := NewRecord("_labels")
		r.Set("name", "tx-label")
		r.Set("color", "red")
		r.Set("icon", "")
		r.Set("type", "custom")
		return tx.SaveRecord(r)
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := db.FindFirstRecord("_labels", "name = ?", "tx-label")
	if err != nil {
		t.Fatal(err)
	}
	if got.GetString("color") != "red" {
		t.Errorf("color = %q, want %q", got.GetString("color"), "red")
	}
}

func TestRunInTransactionRollback(t *testing.T) {
	db := testDB(t)

	// Insert should be rolled back on error.
	err := db.RunInTransaction(func(tx *LorgTx) error {
		r := NewRecord("_labels")
		r.Set("name", "rollback-label")
		r.Set("color", "red")
		r.Set("icon", "")
		r.Set("type", "custom")
		if err := tx.SaveRecord(r); err != nil {
			return err
		}
		return fmt.Errorf("simulated error")
	})
	if err == nil {
		t.Fatal("expected error from transaction")
	}

	records, err := db.FindRecords("_labels", "name = ?", "rollback-label")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Error("expected 0 records after rollback")
	}
}

func TestRecordGettersAndSetters(t *testing.T) {
	r := NewRecord("test")
	r.Set("str", "hello")
	r.Set("num", 42)
	r.Set("flt", 3.14)
	r.Set("boo", true)
	r.Set("nil", nil)

	if r.GetString("str") != "hello" {
		t.Errorf("GetString = %q, want %q", r.GetString("str"), "hello")
	}
	if r.GetInt("num") != 42 {
		t.Errorf("GetInt = %d, want 42", r.GetInt("num"))
	}
	if r.GetFloat("flt") != 3.14 {
		t.Errorf("GetFloat = %f, want 3.14", r.GetFloat("flt"))
	}
	if r.GetBool("boo") != true {
		t.Errorf("GetBool = false, want true")
	}
	if r.GetString("nil") != "" {
		t.Errorf("GetString(nil) = %q, want empty", r.GetString("nil"))
	}
	if r.GetString("missing") != "" {
		t.Errorf("GetString(missing) = %q, want empty", r.GetString("missing"))
	}
}

func TestRecordLoad(t *testing.T) {
	r := NewRecord("test")
	r.Load(map[string]any{
		"method": "GET",
		"url":    "https://example.com",
		"status": 200,
	})
	if r.GetString("method") != "GET" {
		t.Errorf("method = %q, want GET", r.GetString("method"))
	}
	if r.GetInt("status") != 200 {
		t.Errorf("status = %d, want 200", r.GetInt("status"))
	}
}

func TestTableExists(t *testing.T) {
	db := testDB(t)

	exists, err := db.TableExists("_labels")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Error("_labels should exist")
	}

	exists, err = db.TableExists("_nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("_nonexistent should not exist")
	}
}

func TestJsonColumnRoundTrip(t *testing.T) {
	db := testDB(t)

	r := NewRecord("_configs")
	r.Set("key", "test-config")
	r.Set("data", map[string]any{"foo": "bar", "num": 42})
	if err := db.SaveRecord(r); err != nil {
		t.Fatal(err)
	}

	got, err := db.FindRecordById("_configs", r.Id)
	if err != nil {
		t.Fatal(err)
	}
	// JSON columns come back as strings from SQLite.
	dataStr := got.GetString("data")
	if dataStr == "" {
		t.Error("data should not be empty")
	}
}

func TestSeedDefaults(t *testing.T) {
	db := testDB(t)

	if err := db.SeedDefaults(); err != nil {
		t.Fatal(err)
	}

	// Verify settings were created.
	settings, err := db.FindRecords("_settings", "1=1")
	if err != nil {
		t.Fatal(err)
	}
	if len(settings) < 4 {
		t.Errorf("expected at least 4 settings, got %d", len(settings))
	}

	// Verify labels were created.
	labels, err := db.FindRecords("_labels", "1=1")
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) < 9 {
		t.Errorf("expected at least 9 labels, got %d", len(labels))
	}

	// Running again should be a no-op.
	if err := db.SeedDefaults(); err != nil {
		t.Fatal(err)
	}
}
