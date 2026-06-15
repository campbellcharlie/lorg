package app

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"
)

// requireViewingActive returns a 409 response when the UI is currently
// viewing a non-active project. UI write handlers call this at the top
// so the user can't accidentally fire a request from a project they're
// just browsing — the result would write to Active (which is correct)
// but vanish from their view, looking like the action did nothing.
//
// Returns nil when it's safe to proceed. When blocked, writes the 409
// directly and returns a non-nil error so the caller's
// `if err := requireViewingActive(c); err != nil { return err }`
// short-circuits BEFORE the handler attempts to bind/process the body
// (otherwise echo would write a second response to the same connection).
func requireViewingActive(c echo.Context) error {
	if projectDB == nil || projectDB.IsViewingActive() {
		return nil
	}
	active, viewed := projectDB.ActiveAndViewedNames()
	if err := c.JSON(http.StatusConflict, map[string]any{
		"error":        "viewing read-only project",
		"viewed":       viewed,
		"active":       active,
		"hint":         "switch back to active (POST /api/project/switch with empty name) or promote (POST /api/project/setActive)",
		"writeBlocked": true,
	}); err != nil {
		return err
	}
	// Sentinel: response already written; return a non-nil error so the
	// handler bails out without writing a second body.
	return errReadOnlyView
}

// errReadOnlyView is the sentinel error requireViewingActive returns when
// the UI is on a read-only viewer. It's non-nil so the standard
// `if err := requireViewingActive(c); err != nil { return err }` short-
// circuits, but the response has already been written so echo doesn't
// need to render it.
var errReadOnlyView = echo.NewHTTPError(http.StatusConflict, "viewing read-only project")

// ProjectEndpoints registers REST API routes for project management.
func (backend *Backend) ProjectEndpoints(e *echo.Echo) {
	// GET /api/project/info -- current project state
	e.GET("/api/project/info", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, projectDB.Info())
	})

	// GET /api/project/active -- list active projects with proxy info (for frontend)
	e.GET("/api/project/active", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}

		type activeProject struct {
			Name    string `json:"name"`
			Addr    string `json:"addr"`
			ProxyID string `json:"proxyId"`
			Count   int    `json:"count"`
		}

		var projects []activeProject

		// Get projects from running proxies
		ProxyMgr.mu.RLock()
		for id, inst := range ProxyMgr.instances {
			if inst != nil && inst.Project != "" {
				projects = append(projects, activeProject{
					Name:    inst.Project,
					Addr:    inst.Proxy.listenAddr,
					ProxyID: id,
				})
			}
		}
		ProxyMgr.mu.RUnlock()

		// Per-project traffic counts from each project's http_traffic DB
		// (ADR-004 — replaces the _data GROUP BY).
		dbDir := projectDBDir()
		for i := range projects {
			projects[i].Count = projectTrafficCount(dbDir, projects[i].Name)
		}

		if projects == nil {
			projects = []activeProject{}
		}

		return c.JSON(http.StatusOK, map[string]interface{}{
			"projects": projects,
		})
	})

	// GET /api/project/list -- list available .db files in the project directory
	e.GET("/api/project/list", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}

		projectDB.mu.Lock()
		dbDir := projectDB.dbDir
		currentName := projectDB.name
		viewedName := projectDB.viewedName
		if viewedName == "" {
			viewedName = currentName
		}
		projectDB.mu.Unlock()

		if dbDir == "" {
			home, _ := os.UserHomeDir()
			dbDir = home
		}

		type projectEntry struct {
			Name    string `json:"name"`
			Path    string `json:"path"`
			Size    int64  `json:"size"`
			Active  bool   `json:"active"`  // is this the write target
			Viewing bool   `json:"viewing"` // is this what the UI is reading
		}

		var projects []projectEntry
		seen := make(map[string]bool)

		// Scan dbDir for .db files (deduplicates by absolute path)
		scanDir := func(dir string) {
			entries, err := os.ReadDir(dir)
			if err != nil {
				return
			}
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				name := entry.Name()
				if !strings.HasSuffix(name, ".db") {
					continue
				}
				// Skip WAL/SHM/journal files
				if strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-shm") || strings.HasSuffix(name, "-journal") {
					continue
				}
				fullPath := filepath.Join(dir, name)
				if seen[fullPath] {
					continue
				}
				seen[fullPath] = true
				baseName := strings.TrimSuffix(name, ".db")
				info, _ := entry.Info()
				var size int64
				if info != nil {
					size = info.Size()
				}
				projects = append(projects, projectEntry{
					Name:    baseName,
					Path:    fullPath,
					Size:    size,
					Active:  baseName == currentName,
					Viewing: baseName == viewedName,
				})
			}
		}

		scanDir(dbDir)

		// Also scan common project directories relative to dbDir
		for _, extra := range []string{
			filepath.Join(filepath.Dir(dbDir), "Projects"),
		} {
			if extra != dbDir {
				subdirs, err := os.ReadDir(extra)
				if err == nil {
					for _, sd := range subdirs {
						if sd.IsDir() {
							scanDir(filepath.Join(extra, sd.Name()))
						}
					}
				}
			}
		}

		// Scan relative to the working directory (for pentest-framework layout)
		cwd, _ := os.Getwd()
		if cwd != "" && cwd != dbDir {
			projectsDir := filepath.Join(cwd, "Projects")
			subdirs, err := os.ReadDir(projectsDir)
			if err == nil {
				for _, sd := range subdirs {
					if sd.IsDir() {
						scanDir(filepath.Join(projectsDir, sd.Name()))
					}
				}
			}
		}

		if projects == nil {
			projects = []projectEntry{}
		}

		return c.JSON(http.StatusOK, map[string]interface{}{
			"projects":    projects,
			"currentName": currentName, // Active (write target) — kept for back-compat
			"activeName":  currentName,
			"viewedName":  viewedName,
			"dbDir":       dbDir,
		})
	})

	// POST /api/project/switch -- read-only: flip the UI viewer to a different
	// project DB. Writes still go to the Active project (set via /setActive
	// or initial boot). Pass an empty name to clear the viewer and view Active.
	e.POST("/api/project/switch", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}

		var body struct {
			Name  string `json:"name"`
			DbDir string `json:"dbDir"`
		}
		if err := c.Bind(&body); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
		}

		if err := projectDB.SetViewed(body.Name, body.DbDir); err != nil {
			log.Printf("[ProjectSwitch] Error: %v", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}

		if strings.TrimSpace(body.Name) == "" {
			log.Printf("[ProjectSwitch] cleared viewer (now viewing active)")
		} else {
			log.Printf("[ProjectSwitch] viewing: %s (read-only)", body.Name)
		}
		return c.JSON(http.StatusOK, projectDB.Info())
	})

	// POST /api/project/setActive -- destructive: change the write target.
	// All subsequent writes (proxy capture, repeater, intercept, MCP write
	// tools) go to this project. This is the old /switch behavior, now
	// behind an explicit endpoint so it can't be triggered by a casual UI
	// click. Also clears any read-only viewer and persists the choice to
	// _settings.PROJECT_NAME___ so it survives restart.
	e.POST("/api/project/setActive", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}

		var body struct {
			Name  string `json:"name"`
			DbDir string `json:"dbDir"`
		}
		if err := c.Bind(&body); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
		}
		if strings.TrimSpace(body.Name) == "" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "name is required"})
		}

		// Drop the read-only viewer first so SetProject doesn't race against
		// it (and so callers who passed the same name don't end up with
		// viewedDB pointing at the soon-to-be-closed Active handle).
		projectDB.ClearViewed()

		if err := projectDB.SetProject(body.Name, body.DbDir); err != nil {
			log.Printf("[ProjectSetActive] Error: %v", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}

		// Persist active project name so it survives restart.
		rec, err := backend.DB.FindRecordById("_settings", "PROJECT_NAME___")
		if err == nil && rec != nil {
			rec.Set("value", body.Name)
			_ = backend.DB.SaveRecord(rec)
		}

		// Touch the project registry (ADR-003 B1): records existence + last_active.
		if _, rerr := backend.registerProject(body.Name, ""); rerr != nil {
			log.Printf("[ProjectSetActive] registry touch failed: %v", rerr)
		}

		log.Printf("[ProjectSetActive] Active project: %s", body.Name)
		return c.JSON(http.StatusOK, projectDB.Info())
	})

	// POST /api/project/create -- create a new project with its own proxy
	e.POST("/api/project/create", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}

		var body struct {
			Name string `json:"name"`
			Port string `json:"port"` // optional, auto-assigned if empty
		}
		if err := c.Bind(&body); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
		}
		if strings.TrimSpace(body.Name) == "" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "name is required"})
		}

		// Check if a proxy with this project already exists
		ProxyMgr.mu.RLock()
		for _, inst := range ProxyMgr.instances {
			if inst != nil && inst.Project == body.Name {
				ProxyMgr.mu.RUnlock()
				return c.JSON(http.StatusConflict, map[string]string{
					"error": "project already has a proxy running",
					"addr":  inst.Proxy.listenAddr,
				})
			}
		}
		ProxyMgr.mu.RUnlock()

		// Auto-assign port if not specified
		port := body.Port
		if port == "" {
			port = "0" // Let the OS assign
		}

		result, err := backend.startProxyLogic(&ProxyBody{
			HTTP:    "127.0.0.1:" + port,
			Browser: "none",
			Name:    body.Name,
			Project: body.Name,
		})
		if err != nil {
			log.Printf("[ProjectCreate] Error starting proxy: %v", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}

		log.Printf("[ProjectCreate] Created project %s with proxy %v", body.Name, result)
		return c.JSON(http.StatusOK, result)
	})
}
