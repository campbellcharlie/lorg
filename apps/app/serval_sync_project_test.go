package app

import (
	"path/filepath"
	"testing"

	"github.com/campbellcharlie/lorg/internal/lorgdb"
	"github.com/campbellcharlie/lorg/internal/types"
)

// newProjectTagTestBackend builds a Backend backed by a fresh migrated lorgdb
// in a temp dir — enough to exercise the SaveRequestToBackend -> _data ->
// query project:<id> path without a running server.
func newProjectTagTestBackend(t *testing.T) *Backend {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "data.db")
	ldb, err := lorgdb.Open(dbPath)
	if err != nil {
		t.Fatalf("open lorgdb: %v", err)
	}
	if err := ldb.RunMigrations(); err != nil {
		t.Fatalf("migrate lorgdb: %v", err)
	}
	t.Cleanup(func() { ldb.Close() })
	return &Backend{DB: ldb}
}

// TestServalSyncSavePathIsProjectQueryable proves the fix: a row saved through
// the same path ServalSync uses (SaveRequestToBackend with a Project tag) is
// returned by a project:<id> query, and is absent from a different project's
// query. This is the regression guard for the "Serval traffic invisible to
// project-scoped reads" bug.
func TestServalSyncSavePathIsProjectQueryable(t *testing.T) {
	backend := newProjectTagTestBackend(t)

	const project = "acme-engagement"
	body := types.AddRequestBodyType{
		Url:         "https://example.com",
		Request:     "GET /login HTTP/1.1\r\nHost: example.com\r\n\r\n",
		Response:    "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\n\r\n<html></html>",
		GeneratedBy: ServalSource,
		Note:        "Imported from Serval",
		Project:     project,
	}
	if _, err := backend.SaveRequestToBackend(body); err != nil {
		t.Fatalf("SaveRequestToBackend: %v", err)
	}

	// Same project tag -> the imported row is visible.
	got, _, err := backend.executeTrafficQueryWithProject(`req.method.eq:"GET"`, project, 100)
	if err != nil {
		t.Fatalf("query project=%q: %v", project, err)
	}
	if len(got) != 1 {
		t.Fatalf("project:%q query returned %d rows, want 1", project, len(got))
	}

	// A different project tag -> the row is correctly scoped out.
	other, _, err := backend.executeTrafficQueryWithProject(`req.method.eq:"GET"`, "someone-else", 100)
	if err != nil {
		t.Fatalf("query other project: %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("project:someone-else query returned %d rows, want 0", len(other))
	}
}

// TestSaveRequestUntaggedStaysUnscoped guards the backward-compatibility
// contract: callers that leave Project empty (REST add, repeater) produce rows
// that a project-scoped query never returns, exactly as before the fix.
func TestSaveRequestUntaggedStaysUnscoped(t *testing.T) {
	backend := newProjectTagTestBackend(t)

	body := types.AddRequestBodyType{
		Url:         "https://example.com",
		Request:     "GET /health HTTP/1.1\r\nHost: example.com\r\n\r\n",
		GeneratedBy: "repeater/http",
	}
	if _, err := backend.SaveRequestToBackend(body); err != nil {
		t.Fatalf("SaveRequestToBackend: %v", err)
	}

	scoped, _, err := backend.executeTrafficQueryWithProject(`req.method.eq:"GET"`, "any-project", 100)
	if err != nil {
		t.Fatalf("scoped query: %v", err)
	}
	if len(scoped) != 0 {
		t.Fatalf("untagged row leaked into project:any-project query (%d rows)", len(scoped))
	}

	// ...but it is still present in an unscoped query.
	all, _, err := backend.executeTrafficQueryWithProject(`req.method.eq:"GET"`, "", 100)
	if err != nil {
		t.Fatalf("unscoped query: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("unscoped query returned %d rows, want 1", len(all))
	}
}

func TestDeriveProjectFromDBPath(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{"canonical serval layout", "/Users/x/.serval/projects/acme/traffic.db", "acme"},
		{"trailing slash dir", "/var/data/projects/p1/", "p1"},
		{"nested under project", "/srv/projects/eng/sub/traffic.db", "eng"},
		{"no projects segment", "/Users/x/.serval/default/traffic.db", ""},
		{"projects is final segment", "/data/projects", ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeriveProjectFromDBPath(tc.path); got != tc.want {
				t.Errorf("DeriveProjectFromDBPath(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}
