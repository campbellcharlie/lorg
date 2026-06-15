package app

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/campbellcharlie/lorg/internal/types"
)

// countTraffic opens a fresh read connection on a project .db file and returns
// the http_traffic row count. Files are closed by the caller's Close() first so
// every write is flushed before we read.
func countTraffic(t *testing.T, dbFile string) int {
	t.Helper()
	db, err := sql.Open("sqlite", dbFile)
	if err != nil {
		t.Fatalf("open %s: %v", dbFile, err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM http_traffic").Scan(&n); err != nil {
		t.Fatalf("count %s: %v", dbFile, err)
	}
	return n
}

func logRow(t *testing.T, p *ProjectDB, project, path string) {
	t.Helper()
	ud := types.UserData{
		Host:        "example.com",
		Port:        "443",
		IsHTTPS:     true,
		GeneratedBy: "proxy/http",
		ReqJson:     types.RequestData{Method: "GET", Path: path},
		RespJson:    types.ResponseData{Status: 200},
		Project:     project,
	}
	if err := p.LogTraffic(ud, "GET "+path+" HTTP/1.1\r\nHost: example.com\r\n\r\n",
		"HTTP/1.1 200 OK\r\n\r\nok"); err != nil {
		t.Fatalf("LogTraffic(project=%q): %v", project, err)
	}
}

// TestLogTrafficRoutesByProject is the ADR-002 acceptance proof: with the Active
// project set to A, a row carrying Project=B is written to B.db, a row with no
// project goes to the Active A.db, and the Active write target never moves.
// This is the "send a few requests to B while a browser captures into A" case.
func TestLogTrafficRoutesByProject(t *testing.T) {
	dir := t.TempDir()
	p := &ProjectDB{}
	if err := p.Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := p.SetProject("ProjA", dir); err != nil {
		t.Fatalf("SetProject A: %v", err)
	}

	// One addressed write to B, two default writes to the Active (A).
	logRow(t, p, "ProjB", "/b-only")
	logRow(t, p, "", "/a-default")
	logRow(t, p, "ProjA", "/a-explicit") // naming the active project also lands in A

	// Active write target must not have moved.
	if p.name != "ProjA" {
		t.Fatalf("active project moved to %q, want ProjA", p.name)
	}

	// Flush every handle, then read the files back independently.
	p.Close()
	if got := countTraffic(t, filepath.Join(dir, "ProjA.db")); got != 2 {
		t.Errorf("ProjA.db has %d rows, want 2 (default + explicit-active)", got)
	}
	if got := countTraffic(t, filepath.Join(dir, "ProjB.db")); got != 1 {
		t.Errorf("ProjB.db has %d rows, want 1 (the addressed send)", got)
	}
}

// TestLogTrafficEmptyProjectUnchanged guards backward compatibility: with no
// project addressing anywhere, all traffic lands in the single Active DB exactly
// as before ADR-002 — no stray per-project files are created.
func TestLogTrafficEmptyProjectUnchanged(t *testing.T) {
	dir := t.TempDir()
	p := &ProjectDB{}
	if err := p.Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := p.SetProject("Solo", dir); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	logRow(t, p, "", "/x")
	logRow(t, p, "", "/y")

	// No addressed (non-Active) handle should have been opened — the registry
	// stays empty when nothing names a non-Active project.
	if len(p.writeHandles) != 0 {
		t.Errorf("opened %d addressed write handles, want 0: %v", len(p.writeHandles), p.writeHandles)
	}

	p.Close()
	if got := countTraffic(t, filepath.Join(dir, "Solo.db")); got != 2 {
		t.Errorf("Solo.db has %d rows, want 2", got)
	}
}

// TestLogTrafficSupersetColumns is the ADR-004 E1 proof: LogTraffic populates the
// new http_traffic superset columns (fingerprint, generated_by, global_seq) so
// projectDB can serve the readers currently tied to _data.
func TestLogTrafficSupersetColumns(t *testing.T) {
	dir := t.TempDir()
	p := &ProjectDB{}
	if err := p.Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := p.SetProject("Sup", dir); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	ud := types.UserData{
		Host: "example.com", Port: "443", IsHTTPS: true, GeneratedBy: "proxy/http",
		ReqJson:  types.RequestData{Method: "GET", Path: "/s"},
		RespJson: types.ResponseData{Status: 200, Mime: "text/html"},
	}
	if err := p.LogTraffic(ud, "GET /s HTTP/1.1\r\nHost: example.com\r\n\r\n",
		"HTTP/1.1 200 OK\r\nContent-Type: text/html\r\n\r\n<html>hi</html>"); err != nil {
		t.Fatalf("LogTraffic: %v", err)
	}
	p.Close()

	db, err := sql.Open("sqlite", filepath.Join(dir, "Sup.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	var fp, gb string
	var seq int64
	if err := db.QueryRow("SELECT fingerprint, generated_by, global_seq FROM http_traffic LIMIT 1").Scan(&fp, &gb, &seq); err != nil {
		t.Fatalf("scan superset cols: %v", err)
	}
	if fp == "" {
		t.Errorf("fingerprint empty — clustering tools depend on it")
	}
	if gb != "proxy/http" {
		t.Errorf("generated_by = %q, want proxy/http", gb)
	}
	if seq == 0 {
		t.Errorf("global_seq not set — cross-project ordering depends on it")
	}
}
