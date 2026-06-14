package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campbellcharlie/lorg/internal/lorgdb"
)

// TestProjectInUseGiven covers the pure in-use core (ADR-003 B2).
func TestProjectInUseGiven(t *testing.T) {
	// Active write target is in use.
	if used, _ := projectInUseGiven("Acme", "Acme", nil); !used {
		t.Errorf("active project should be in use")
	}
	// Proxy-bound is in use.
	if used, reason := projectInUseGiven("Acme", "Other", map[string]string{"127.0.0.1:9001": "Acme"}); !used || reason == "" {
		t.Errorf("proxy-bound project should be in use with a reason")
	}
	// Otherwise free.
	if used, _ := projectInUseGiven("Acme", "Other", map[string]string{"127.0.0.1:9001": "Else"}); used {
		t.Errorf("unbound, non-active project should be free")
	}
}

// TestDeleteProjectGuardedAndComplete is the ADR-003 B3 proof: delete needs
// confirm, and a confirmed delete removes the DB files, the project's sessions,
// and the registry row.
func TestDeleteProjectGuardedAndComplete(t *testing.T) {
	backend := newProjectTagTestBackend(t)
	dir := t.TempDir()
	const name = "DeleteMe"

	// Set up: a DB file on disk, a registry row, and a session in this project.
	dbFile := filepath.Join(dir, name+".db")
	writeDummyDB(t, dbFile)
	writeDummyDB(t, dbFile+"-wal")
	if _, err := backend.registerProject(name, dbFile); err != nil {
		t.Fatalf("register: %v", err)
	}
	sess := lorgdb.NewRecord("_sessions")
	sess.Set("name", "jar")
	sess.Set("project", name)
	if err := backend.DB.SaveRecord(sess); err != nil {
		t.Fatalf("save session: %v", err)
	}

	// Guard: confirm=false is refused and changes nothing.
	if err := backend.deleteProject(name, dir, false); err == nil {
		t.Fatalf("delete without confirm should be refused")
	}
	if _, err := os.Stat(dbFile); err != nil {
		t.Fatalf("db file removed despite unconfirmed delete")
	}

	// Confirmed delete tears everything down.
	if err := backend.deleteProject(name, dir, true); err != nil {
		t.Fatalf("confirmed delete: %v", err)
	}
	if _, err := os.Stat(dbFile); !os.IsNotExist(err) {
		t.Errorf("db file still present after delete")
	}
	if _, err := os.Stat(dbFile + "-wal"); !os.IsNotExist(err) {
		t.Errorf("wal sidecar still present after delete")
	}
	if recs, _ := backend.DB.FindRecords("_sessions", "project = ?", name); len(recs) != 0 {
		t.Errorf("project sessions not removed: %d remain", len(recs))
	}
	if _, err := backend.getProjectMeta(name, dir); err == nil {
		t.Errorf("registry row still present after delete")
	}
}

// TestArchiveRefusesNothingFree confirms archive flips status when a project is
// free (the in-use refusal path runs against live globals and is covered by
// TestProjectInUseGiven).
func TestArchiveFreeProject(t *testing.T) {
	backend := newProjectTagTestBackend(t)
	dir := t.TempDir()
	if _, err := backend.registerProject("Old", filepath.Join(dir, "Old.db")); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := backend.archiveProject("Old"); err != nil {
		t.Fatalf("archive: %v", err)
	}
	m, err := backend.getProjectMeta("Old", dir)
	if err != nil {
		t.Fatalf("getProjectMeta: %v", err)
	}
	if m.Status != projectStatusArchived {
		t.Errorf("status = %q, want archived", m.Status)
	}
}

// TestAutoArchiveStale is the ADR-003 B5 proof: projects inactive beyond the
// cutoff are archived, recently-active ones are left alone.
func TestAutoArchiveStale(t *testing.T) {
	backend := newProjectTagTestBackend(t)
	dir := t.TempDir()

	if _, err := backend.registerProject("Fresh", filepath.Join(dir, "Fresh.db")); err != nil {
		t.Fatalf("register Fresh: %v", err)
	}
	if _, err := backend.registerProject("Stale", filepath.Join(dir, "Stale.db")); err != nil {
		t.Fatalf("register Stale: %v", err)
	}
	// Backdate Stale's last_active to 90 days ago.
	rec, _ := backend.DB.FindFirstRecord("_projects", "name = ?", "Stale")
	d := projectDataOf(rec)
	d["lastActive"] = time.Now().AddDate(0, 0, -90).UTC().Format(time.RFC3339)
	rec.Set("data", d)
	if err := backend.DB.SaveRecord(rec); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	archived, err := backend.autoArchiveStale(dir, 30, time.Now())
	if err != nil {
		t.Fatalf("autoArchiveStale: %v", err)
	}
	if len(archived) != 1 || archived[0] != "Stale" {
		t.Fatalf("archived = %v, want [Stale]", archived)
	}
	if m, _ := backend.getProjectMeta("Stale", dir); m.Status != projectStatusArchived {
		t.Errorf("Stale not archived")
	}
	if m, _ := backend.getProjectMeta("Fresh", dir); m.Status != projectStatusActive {
		t.Errorf("Fresh wrongly archived")
	}
}
