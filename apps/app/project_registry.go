package app

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/campbellcharlie/lorg/internal/lorgdb"
)

// Project registry (ADR-003 B1). The _projects table is the authoritative list
// of projects with lifecycle metadata, replacing the previous filesystem-scan.
// Lifecycle/metadata live in the row's `data` JSON column so no schema migration
// is needed; size/traffic counts are computed from disk on read (never stale).

const (
	projectStatusActive   = "active"
	projectStatusArchived = "archived"
)

// ProjectMeta is the registry view of a project returned to callers (REST/MCP).
type ProjectMeta struct {
	Name         string `json:"name"`
	Path         string `json:"path"`
	Status       string `json:"status"`
	Created      string `json:"created"`
	LastActive   string `json:"lastActive"`
	TargetHost   string `json:"targetHost,omitempty"`
	Notes        string `json:"notes,omitempty"`
	SizeBytes    int64  `json:"sizeBytes"`
	TrafficCount int    `json:"trafficCount,omitempty"`
}

func projectDataOf(rec *lorgdb.Record) map[string]any {
	if m, ok := rec.Get("data").(map[string]any); ok && m != nil {
		return m
	}
	return map[string]any{}
}

func dataStr(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// registerProject upserts the registry row for name and stamps last_active.
// Creating a row defaults status=active. Used on create/setActive and by the
// list reconcile backfill.
func (backend *Backend) registerProject(name, path string) (*lorgdb.Record, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("project name required")
	}
	now := time.Now().UTC().Format(time.RFC3339)

	rec, _ := backend.DB.FindFirstRecord("_projects", "name = ?", name)
	if rec == nil {
		rec = lorgdb.NewRecord("_projects")
		rec.Set("name", name)
		rec.Set("path", path)
		rec.Set("data", map[string]any{"status": projectStatusActive, "lastActive": now})
	} else {
		if path != "" {
			rec.Set("path", path)
		}
		d := projectDataOf(rec)
		d["lastActive"] = now
		if dataStr(d, "status") == "" {
			d["status"] = projectStatusActive
		}
		rec.Set("data", d)
	}
	if err := backend.DB.SaveRecord(rec); err != nil {
		return nil, fmt.Errorf("registerProject: %w", err)
	}
	return rec, nil
}

// metaFromRecord builds a ProjectMeta, filling size from disk if dbDir is set.
func metaFromRecord(rec *lorgdb.Record, dbDir string) ProjectMeta {
	d := projectDataOf(rec)
	name := rec.GetString("name")
	m := ProjectMeta{
		Name:       name,
		Path:       rec.GetString("path"),
		Created:    rec.GetString("created"),
		Status:     orDefault(dataStr(d, "status"), projectStatusActive),
		LastActive: dataStr(d, "lastActive"),
		TargetHost: dataStr(d, "targetHost"),
		Notes:      dataStr(d, "notes"),
	}
	if dbDir != "" {
		if fi, err := os.Stat(filepath.Join(dbDir, sanitizeProjectName(name)+".db")); err == nil {
			m.SizeBytes = fi.Size()
		}
	}
	return m
}

// listProjects returns the registry, reconciled with on-disk .db files: any DB
// without a registry row is backfilled (so projects created before B1, or out of
// band, still appear). includeArchived controls whether archived rows are shown.
func (backend *Backend) listProjects(dbDir string, includeArchived bool) ([]ProjectMeta, error) {
	recs, err := backend.DB.FindRecords("_projects", "1=1")
	if err != nil {
		return nil, fmt.Errorf("listProjects: %w", err)
	}
	byName := make(map[string]*lorgdb.Record, len(recs))
	for _, r := range recs {
		byName[r.GetString("name")] = r
	}

	// Backfill: register any on-disk project DB missing from the registry.
	if dbDir != "" {
		if entries, derr := os.ReadDir(dbDir); derr == nil {
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".db") {
					continue
				}
				n := strings.TrimSuffix(e.Name(), ".db")
				if n == "TemporaryProject" {
					continue
				}
				if _, ok := byName[n]; !ok {
					if r, rerr := backend.registerProject(n, filepath.Join(dbDir, e.Name())); rerr == nil {
						byName[n] = r
					}
				}
			}
		}
	}

	out := make([]ProjectMeta, 0, len(byName))
	for _, r := range byName {
		m := metaFromRecord(r, dbDir)
		if !includeArchived && m.Status == projectStatusArchived {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// getProjectMeta returns one project's metadata, including a live traffic-row
// count from its DB (single-project, so the count query is affordable).
func (backend *Backend) getProjectMeta(name, dbDir string) (*ProjectMeta, error) {
	rec, err := backend.DB.FindFirstRecord("_projects", "name = ?", strings.TrimSpace(name))
	if err != nil || rec == nil {
		return nil, fmt.Errorf("project %q not found in registry", name)
	}
	m := metaFromRecord(rec, dbDir)
	m.TrafficCount = projectTrafficCount(dbDir, name)
	return &m, nil
}

// setProjectStatus flips a project's lifecycle status (archive/unarchive).
func (backend *Backend) setProjectStatus(name, status string) error {
	rec, err := backend.DB.FindFirstRecord("_projects", "name = ?", strings.TrimSpace(name))
	if err != nil || rec == nil {
		return fmt.Errorf("project %q not found in registry", name)
	}
	d := projectDataOf(rec)
	d["status"] = status
	rec.Set("data", d)
	return backend.DB.SaveRecord(rec)
}

// projectTrafficCount opens a read-only connection to a project's DB and counts
// captured rows. Returns 0 if the DB is missing or unreadable.
func projectTrafficCount(dbDir, name string) int {
	if dbDir == "" {
		return 0
	}
	dbFile := filepath.Join(dbDir, sanitizeProjectName(name)+".db")
	if _, err := os.Stat(dbFile); err != nil {
		return 0
	}
	db, err := sql.Open("sqlite", "file:"+dbFile+"?mode=ro&_query_only=on")
	if err != nil {
		return 0
	}
	defer db.Close()
	var n int
	_ = db.QueryRow("SELECT COUNT(*) FROM http_traffic").Scan(&n)
	return n
}

// projectInUseGiven is the pure core of in-use detection (ADR-003 B2): a project
// is in use if it is the active write target or a proxy listener is bound to it.
// proxyProjects maps listener address -> bound project name.
func projectInUseGiven(name, activeName string, proxyProjects map[string]string) (bool, string) {
	s := sanitizeProjectName(name)
	if s != "" && s == sanitizeProjectName(activeName) {
		return true, "it is the active write target — switch to another project first"
	}
	for addr, proj := range proxyProjects {
		if sanitizeProjectName(proj) == s {
			return true, fmt.Sprintf("a proxy listener is bound to it (%s)", addr)
		}
	}
	return false, ""
}

// projectInUse gathers live binding state (active target + running proxies) and
// reports whether the named project is in use, with a human reason. Destructive
// lifecycle ops (archive/delete) refuse in-use projects.
func projectInUse(name string) (bool, string) {
	projectDB.mu.RLock()
	active := projectDB.name
	projectDB.mu.RUnlock()

	proxies := make(map[string]string)
	ProxyMgr.mu.RLock()
	for _, inst := range ProxyMgr.instances {
		if inst != nil {
			proxies[inst.Proxy.listenAddr] = inst.Project
		}
	}
	ProxyMgr.mu.RUnlock()

	return projectInUseGiven(name, active, proxies)
}

// deregisterProject removes a project's registry row (used by hard delete).
func (backend *Backend) deregisterProject(name string) error {
	rec, err := backend.DB.FindFirstRecord("_projects", "name = ?", strings.TrimSpace(name))
	if err != nil || rec == nil {
		return nil // already gone
	}
	return backend.DB.DeleteRecord("_projects", rec.Id)
}

// archiveProject hides a project from the active list while keeping its data
// (ADR-003 B3). Refuses an in-use project — switch away / stop its proxy first.
func (backend *Backend) archiveProject(name string) error {
	if inUse, reason := projectInUse(name); inUse {
		return fmt.Errorf("cannot archive %q: %s", name, reason)
	}
	return backend.setProjectStatus(name, projectStatusArchived)
}

// unarchiveProject restores an archived project to active status.
func (backend *Backend) unarchiveProject(name string) error {
	return backend.setProjectStatus(name, projectStatusActive)
}

// removeProjectFiles deletes a project's SQLite files (.db + WAL/SHM sidecars).
func removeProjectFiles(dbDir, name string) error {
	sanitized := sanitizeProjectName(name)
	if sanitized == "" || dbDir == "" {
		return fmt.Errorf("project name and directory required")
	}
	base := filepath.Join(dbDir, sanitized+".db")
	var firstErr error
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(base + suffix); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// deleteProject permanently removes a project: its SQLite files, its sessions,
// and its registry row (ADR-003 B3). Engagement data is evidence, so this is
// guarded: confirm must be true, and an in-use project (active target or a bound
// proxy) is refused — the caller must switch away / stop the proxy first.
func (backend *Backend) deleteProject(name, dbDir string, confirm bool) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("project name required")
	}
	if !confirm {
		return fmt.Errorf("delete requires confirm=true — this permanently removes %q's captured traffic and sessions", name)
	}
	if inUse, reason := projectInUse(name); inUse {
		return fmt.Errorf("cannot delete %q: %s", name, reason)
	}

	// Close any open handle so the files aren't locked, then remove them.
	projectDB.closeProjectHandle(name)
	if err := removeProjectFiles(dbDir, name); err != nil {
		return fmt.Errorf("delete project files: %w", err)
	}

	// Remove the project's sessions (lorgdb) and its registry row.
	if recs, err := backend.DB.FindRecords("_sessions", "project = ?", name); err == nil {
		for _, r := range recs {
			_ = backend.DB.DeleteRecord("_sessions", r.Id)
		}
	}
	return backend.deregisterProject(name)
}
