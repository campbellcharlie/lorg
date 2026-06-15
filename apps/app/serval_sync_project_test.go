package app

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/campbellcharlie/lorg/internal/lorgdb"
	"github.com/campbellcharlie/lorg/internal/types"
)

// useGlobalProjectDB points the package-level projectDB singleton at a temp dir
// with the given active project, so SaveRequestToBackend's async http_traffic
// write (and the union readers) have somewhere to land. Reset on cleanup.
func useGlobalProjectDB(t *testing.T, dir, active string) {
	t.Helper()
	projectDB.Close()
	if err := projectDB.Init(dir); err != nil {
		t.Fatalf("projectDB.Init: %v", err)
	}
	if err := projectDB.SetProject(active, dir); err != nil {
		t.Fatalf("projectDB.SetProject: %v", err)
	}
	t.Cleanup(func() { projectDB.Close() })
}

// queryUntil polls the union query until it returns wantN rows (or times out),
// to ride out the async logging worker pool.
func queryUntil(t *testing.T, backend *Backend, query, project string, wantN int) int {
	t.Helper()
	for i := 0; i < 200; i++ {
		got, _, err := backend.executeTrafficQueryWithProject(query, project, 100)
		if err != nil {
			t.Fatalf("query project=%q: %v", project, err)
		}
		if len(got) == wantN {
			return len(got)
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, _, _ := backend.executeTrafficQueryWithProject(query, project, 100)
	return len(got)
}

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
	useGlobalProjectDB(t, t.TempDir(), project)

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

	// Same project tag -> the imported row is visible (via http_traffic now).
	if got := queryUntil(t, backend, `req.method.eq:"GET"`, project, 1); got != 1 {
		t.Fatalf("project:%q query returned %d rows, want 1", project, got)
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
	// Untagged rows route to the active project (a neutral one here).
	useGlobalProjectDB(t, t.TempDir(), "neutral-active")

	body := types.AddRequestBodyType{
		Url:         "https://example.com",
		Request:     "GET /health HTTP/1.1\r\nHost: example.com\r\n\r\n",
		GeneratedBy: "repeater/http",
	}
	if _, err := backend.SaveRequestToBackend(body); err != nil {
		t.Fatalf("SaveRequestToBackend: %v", err)
	}

	// Present in an unscoped query (reads all project DBs).
	if all := queryUntil(t, backend, `req.method.eq:"GET"`, "", 1); all != 1 {
		t.Fatalf("unscoped query returned %d rows, want 1", all)
	}

	// Absent from a query scoped to a DIFFERENT project.
	scoped, _, err := backend.executeTrafficQueryWithProject(`req.method.eq:"GET"`, "any-project", 100)
	if err != nil {
		t.Fatalf("scoped query: %v", err)
	}
	if len(scoped) != 0 {
		t.Fatalf("untagged row leaked into project:any-project query (%d rows)", len(scoped))
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
