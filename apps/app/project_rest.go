package app

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/glitchedgitz/pocketbase/apis"
	"github.com/glitchedgitz/pocketbase/core"
	"github.com/labstack/echo/v5"
)

// ProjectEndpoints registers REST API routes for project management.
func (backend *Backend) ProjectEndpoints(e *core.ServeEvent) error {
	// GET /api/project/info — current project state
	e.Router.AddRoute(echo.Route{
		Method: http.MethodGet,
		Path:   "/api/project/info",
		Handler: func(c echo.Context) error {
			if err := requireLocalhost(c); err != nil {
				return err
			}
			return c.JSON(http.StatusOK, projectDB.Info())
		},
		Middlewares: []echo.MiddlewareFunc{apis.ActivityLogger(backend.App)},
	})

	// GET /api/project/active — list active projects with proxy info (for frontend)
	e.Router.AddRoute(echo.Route{
		Method: http.MethodGet,
		Path:   "/api/project/active",
		Handler: func(c echo.Context) error {
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

			// Get traffic counts per project in a single query
			if len(projects) > 0 {
				db := backend.App.Dao().DB()
				countRows, err := db.NewQuery(`SELECT COALESCE(project,''), COUNT(*) FROM _data WHERE project != '' GROUP BY project`).Rows()
				if err == nil {
					counts := make(map[string]int)
					for countRows.Next() {
						var name string
						var count int
						if countRows.Scan(&name, &count) == nil {
							counts[name] = count
						}
					}
					countRows.Close()
					for i := range projects {
						projects[i].Count = counts[projects[i].Name]
					}
				}
			}

			if projects == nil {
				projects = []activeProject{}
			}

			return c.JSON(http.StatusOK, map[string]interface{}{
				"projects": projects,
			})
		},
		Middlewares: []echo.MiddlewareFunc{apis.ActivityLogger(backend.App)},
	})

	// GET /api/project/list — list available .db files in the project directory
	e.Router.AddRoute(echo.Route{
		Method: http.MethodGet,
		Path:   "/api/project/list",
		Handler: func(c echo.Context) error {
			if err := requireLocalhost(c); err != nil {
				return err
			}

			projectDB.mu.Lock()
			dbDir := projectDB.dbDir
			currentName := projectDB.name
			projectDB.mu.Unlock()

			if dbDir == "" {
				home, _ := os.UserHomeDir()
				dbDir = home
			}

			type projectEntry struct {
				Name   string `json:"name"`
				Path   string `json:"path"`
				Size   int64  `json:"size"`
				Active bool   `json:"active"`
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
						Name:   baseName,
						Path:   fullPath,
						Size:   size,
						Active: baseName == currentName,
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
				"currentName": currentName,
				"dbDir":       dbDir,
			})
		},
		Middlewares: []echo.MiddlewareFunc{apis.ActivityLogger(backend.App)},
	})

	// POST /api/project/switch — switch to a different project DB
	e.Router.AddRoute(echo.Route{
		Method: http.MethodPost,
		Path:   "/api/project/switch",
		Handler: func(c echo.Context) error {
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

			if err := projectDB.SetProject(body.Name, body.DbDir); err != nil {
				log.Printf("[ProjectSwitch] Error: %v", err)
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
			}

			// Update PocketBase _settings with new project name
			dao := backend.App.Dao()
			rec, err := dao.FindRecordById("_settings", "PROJECT_NAME___")
			if err == nil && rec != nil {
				rec.Set("value", body.Name)
				_ = dao.SaveRecord(rec)
			}

			log.Printf("[ProjectSwitch] Switched to project: %s", body.Name)
			return c.JSON(http.StatusOK, projectDB.Info())
		},
		Middlewares: []echo.MiddlewareFunc{apis.ActivityLogger(backend.App)},
	})

	// POST /api/project/create — create a new project with its own proxy
	e.Router.AddRoute(echo.Route{
		Method: http.MethodPost,
		Path:   "/api/project/create",
		Handler: func(c echo.Context) error {
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
		},
		Middlewares: []echo.MiddlewareFunc{apis.ActivityLogger(backend.App)},
	})

	return nil
}
