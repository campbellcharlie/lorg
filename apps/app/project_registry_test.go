package app

import (
	"os"
	"path/filepath"
	"testing"
)

// TestProjectRegistry is the ADR-003 B1 proof: registerProject upserts a row,
// listProjects reconciles with on-disk .db files (backfilling a project that was
// never registered), archived projects are hidden unless requested, and metadata
// (status, size) is reported.
func TestProjectRegistry(t *testing.T) {
	backend := newProjectTagTestBackend(t)
	dir := t.TempDir()

	// A registered project.
	if _, err := backend.registerProject("Alpha", filepath.Join(dir, "Alpha.db")); err != nil {
		t.Fatalf("registerProject: %v", err)
	}
	// Touch again — should not duplicate, just update last_active.
	if _, err := backend.registerProject("Alpha", ""); err != nil {
		t.Fatalf("re-register: %v", err)
	}

	// An on-disk DB that was never registered (simulates a pre-B1 project).
	writeDummyDB(t, filepath.Join(dir, "Beta.db"))
	// TemporaryProject must be ignored by the backfill.
	writeDummyDB(t, filepath.Join(dir, "TemporaryProject.db"))

	list, err := backend.listProjects(dir, false)
	if err != nil {
		t.Fatalf("listProjects: %v", err)
	}
	names := map[string]ProjectMeta{}
	for _, m := range list {
		names[m.Name] = m
	}
	if _, ok := names["Alpha"]; !ok {
		t.Errorf("Alpha missing from list")
	}
	if _, ok := names["Beta"]; !ok {
		t.Errorf("Beta (on-disk, unregistered) was not backfilled")
	}
	if _, ok := names["TemporaryProject"]; ok {
		t.Errorf("TemporaryProject should be excluded from the registry")
	}
	if names["Beta"].SizeBytes == 0 {
		t.Errorf("Beta size not reported")
	}

	// Archive Alpha — hidden by default, shown when includeArchived.
	if err := backend.setProjectStatus("Alpha", projectStatusArchived); err != nil {
		t.Fatalf("setProjectStatus: %v", err)
	}
	def, _ := backend.listProjects(dir, false)
	for _, m := range def {
		if m.Name == "Alpha" {
			t.Errorf("archived Alpha still listed by default")
		}
	}
	all, _ := backend.listProjects(dir, true)
	found := false
	for _, m := range all {
		if m.Name == "Alpha" {
			found = true
			if m.Status != projectStatusArchived {
				t.Errorf("Alpha status = %q, want archived", m.Status)
			}
		}
	}
	if !found {
		t.Errorf("archived Alpha not shown even with includeArchived")
	}

	// Deregister removes the row.
	if err := backend.deregisterProject("Alpha"); err != nil {
		t.Fatalf("deregister: %v", err)
	}
	if _, err := backend.getProjectMeta("Alpha", dir); err == nil {
		t.Errorf("Alpha still resolvable after deregister")
	}
}

// writeDummyDB creates a minimal project DB file on disk so backfill/size logic
// has something real to stat.
func writeDummyDB(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("SQLite format 3\x00"), 0644); err != nil {
		t.Fatalf("write dummy db %s: %v", path, err)
	}
}
