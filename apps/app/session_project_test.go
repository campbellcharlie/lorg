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

func jarCookies(t *testing.T, backend *Backend, project, name string) map[string]any {
	t.Helper()
	rec, err := backend.findSessionByNameInProject(project, name)
	if err != nil {
		t.Fatalf("lookup %q/%q: %v", project, name, err)
	}
	m, _ := rec.Get("cookies").(map[string]any)
	return m
}

// TestCopyCookiesBetweenJars proves Slice 3: an explicit, selective transfer
// from one project's jar to another. Only allowlisted names move, existing
// destination cookies are preserved, and the source jar is left untouched.
func TestCopyCookiesBetweenJars(t *testing.T) {
	backend := newProjectTagTestBackend(t)

	// Source jar (project A) has an SSO token plus a host-specific cookie.
	srcRec := lorgdb.NewRecord("_sessions")
	srcRec.Set("name", "auth")
	srcRec.Set("project", "ProjA")
	srcRec.Set("cookies", map[string]string{"sso": "shared-token", "acme_sid": "a-only"})
	srcRec.Set("active", true)
	if err := backend.DB.SaveRecord(srcRec); err != nil {
		t.Fatalf("save src: %v", err)
	}
	// Destination jar (project B) already has its own cookie.
	dstRec := lorgdb.NewRecord("_sessions")
	dstRec.Set("name", "auth")
	dstRec.Set("project", "ProjB")
	dstRec.Set("cookies", map[string]string{"b_sid": "b-existing"})
	dstRec.Set("active", true)
	if err := backend.DB.SaveRecord(dstRec); err != nil {
		t.Fatalf("save dst: %v", err)
	}

	// Copy ONLY the shared SSO token from A's active jar to B's active jar.
	copied, _, _, err := backend.copyCookiesBetween("ProjA", "", "ProjB", "", []string{"sso"})
	if err != nil {
		t.Fatalf("copyCookiesBetween: %v", err)
	}
	if len(copied) != 1 || copied[0] != "sso" {
		t.Fatalf("copied = %v, want [sso]", copied)
	}

	dstJar := jarCookies(t, backend, "ProjB", "auth")
	if dstJar["sso"] != "shared-token" {
		t.Errorf("destination missing copied sso token: %v", dstJar)
	}
	if dstJar["b_sid"] != "b-existing" {
		t.Errorf("copy clobbered destination's existing cookie: %v", dstJar)
	}
	if _, leaked := dstJar["acme_sid"]; leaked {
		t.Errorf("non-allowlisted cookie acme_sid leaked into destination: %v", dstJar)
	}

	// Source jar is unchanged.
	srcJar := jarCookies(t, backend, "ProjA", "auth")
	if srcJar["acme_sid"] != "a-only" || srcJar["sso"] != "shared-token" {
		t.Errorf("source jar was mutated by copy: %v", srcJar)
	}
}
