package app

import (
	"testing"

	"github.com/campbellcharlie/lorg/internal/lorgdb"
)

// mkSession inserts a session row directly so the test exercises the storage +
// resolution layer the send path relies on, without building MCP requests.
func mkSession(t *testing.T, backend *Backend, project, name string, active bool, sid string) {
	t.Helper()
	rec := lorgdb.NewRecord("_sessions")
	rec.Set("name", name)
	rec.Set("project", project)
	rec.Set("cookies", map[string]string{"sid": sid})
	rec.Set("active", active)
	if err := backend.DB.SaveRecord(rec); err != nil {
		t.Fatalf("save session %q/%q: %v", project, name, err)
	}
}

func activeSid(t *testing.T, backend *Backend, project string) string {
	t.Helper()
	rec, err := backend.findActiveSessionInProject(project)
	if err != nil {
		t.Fatalf("findActiveSessionInProject(%q): %v", project, err)
	}
	cookies, _ := rec.Get("cookies").(map[string]any)
	sid, _ := cookies["sid"].(string)
	return sid
}

// TestSessionsAreProjectScoped is the ADR-002 Slice 2 proof: the same session
// name can exist independently in two projects (the migration moved uniqueness
// to (project,name)), each project can have its OWN active session at the same
// time, and resolving the active jar by project returns that project's cookies —
// so a B-addressed send injects B's auth, not the default project's.
func TestSessionsAreProjectScoped(t *testing.T) {
	backend := newProjectTagTestBackend(t)

	// Same name "auth" in two projects, each active within its own project.
	mkSession(t, backend, "", "auth", true, "default-sid")
	mkSession(t, backend, "ProjB", "auth", true, "projb-sid")

	// Both active rows coexist — uniqueness is (project,name), not (name).
	if got := activeSid(t, backend, ""); got != "default-sid" {
		t.Errorf("default project active sid = %q, want default-sid", got)
	}
	if got := activeSid(t, backend, "ProjB"); got != "projb-sid" {
		t.Errorf("ProjB active sid = %q, want projb-sid", got)
	}

	// A project with no session resolves to an error, not someone else's jar.
	if _, err := backend.findActiveSessionInProject("ProjC"); err == nil {
		t.Errorf("ProjC unexpectedly resolved an active session; jars are leaking across projects")
	}

	// Name lookup is project-scoped too.
	if _, err := backend.findSessionByNameInProject("ProjB", "auth"); err != nil {
		t.Errorf("findSessionByNameInProject(ProjB, auth): %v", err)
	}
	if _, err := backend.findSessionByNameInProject("ProjC", "auth"); err == nil {
		t.Errorf("found 'auth' in ProjC, but it was only created in default + ProjB")
	}
}
