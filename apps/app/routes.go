package app

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/campbellcharlie/lorg/internal/lorgdb"
	"github.com/labstack/echo/v4"
)

// RegisterRoutes wires up all HTTP endpoints on the Echo instance.
func (backend *Backend) RegisterRoutes(e *echo.Echo) {
	// Info
	backend.Info(e)
	backend.CWDContent(e)
	backend.CWDBrowse(e)
	backend.CWDReadFile(e)

	// Labels
	backend.LabelAttach(e)
	backend.LabelDelete(e)
	backend.LabelNew(e)

	// Frontend
	// (registered last via deferred call — must be after all API routes)
	defer backend.BindFrontend(e)

	// Sitemap
	backend.SitemapNew(e)
	backend.SitemapFetch(e)

	// Send Raw Request
	backend.SendRawRequest(e)
	backend.SendHttpRaw(e)

	// Testing
	backend.TextSQL(e)

	// File Operations
	backend.SaveFile(e)
	backend.ReadFile(e)

	// System
	backend.DownloadCert(e)
	backend.SearchRegex(e)
	backend.FileWatcher(e)

	// Template
	backend.TemplatesList(e)
	backend.TemplatesNew(e)
	backend.TemplatesDelete(e)

	// Commands
	backend.RunCommand(e)
	backend.Tools(e)

	// Cook (removed -- stub endpoints return 410 Gone)
	backend.CookSearch(e)
	backend.CookApplyMethods(e)
	backend.CookGenerate(e)

	// Playground
	backend.PlaygroundNew(e)
	backend.PlaygroundDelete(e)
	backend.PlaygroundAddChild(e)

	// Proxies
	backend.StartProxy(e)
	backend.StopProxy(e)
	backend.RestartProxy(e)
	backend.ListProxies(e)
	backend.ScreenshotProxy(e)
	backend.ClickProxy(e)
	backend.GetElementsProxy(e)
	backend.ListChromeTabs(e)
	backend.OpenChromeTab(e)
	backend.NavigateChromeTab(e)
	backend.ActivateTab(e)
	backend.CloseTab(e)
	backend.ReloadTab(e)
	backend.GoBack(e)
	backend.GoForward(e)
	backend.TypeTextProxy(e)
	backend.WaitForSelectorProxy(e)
	backend.EvaluateProxy(e)

	// Other
	backend.AddRequest(e)
	backend.InterceptEndpoints(e)
	backend.FiltersCheck(e)

	// Repeater
	backend.SendRepeater(e)

	// Traffic list
	backend.TrafficList(e)

	// Traffic detail
	backend.TrafficDetail(e)

	// Scope REST endpoints
	backend.ScopeEndpoints(e)

	// Match & Replace
	backend.MatchReplaceEndpoints(e)

	// Modify
	backend.ModifyRequest(e)

	// Parse
	backend.ParseRaw(e)

	// Extractor
	backend.ExtractDataEndpoint(e)

	// Project management
	backend.ProjectEndpoints(e)

	// MCP
	backend.MCPEndpoint(e)

	// Xterm (Terminal) - only if explicitly enabled
	if backend.Config.EnableTerminal {
		backend.RegisterXtermRoutes(e)
	}

	// Generic collection CRUD (replaces PocketBase's auto-generated REST API)
	backend.registerCollectionCRUD(e)
}

// registerCollectionCRUD provides basic CRUD endpoints for PocketBase collections,
// replacing the auto-generated REST API that PocketBase provided.
func (backend *Backend) registerCollectionCRUD(e *echo.Echo) {
	// List records
	e.GET("/api/collections/:collection/records", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}
		table := c.Param("collection")
		filter := c.QueryParam("filter")
		sort := c.QueryParam("sort")

		where := "1=1"
		var args []any
		if filter != "" {
			where = filter
		}

		var records []*lorgdb.Record
		var err error
		if sort != "" {
			records, err = backend.DB.FindRecordsSorted(table, where, sort, 0, 0, args...)
		} else {
			records, err = backend.DB.FindRecords(table, where, args...)
		}
		if err != nil {
			return c.JSON(http.StatusOK, map[string]any{"items": []any{}, "totalItems": 0})
		}

		items := make([]map[string]any, 0, len(records))
		for _, r := range records {
			item := map[string]any{"id": r.Id, "created": r.Created, "updated": r.Updated}
			for k, v := range r.Data {
				item[k] = v
			}
			items = append(items, item)
		}
		return c.JSON(http.StatusOK, map[string]any{
			"items":      items,
			"totalItems": len(items),
			"page":       1,
			"perPage":    len(items),
		})
	})

	// Get single record
	e.GET("/api/collections/:collection/records/:id", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}
		table := c.Param("collection")
		id := c.Param("id")
		record, err := backend.DB.FindRecordById(table, id)
		if err != nil {
			return echo.NewHTTPError(http.StatusNotFound, "record not found")
		}
		item := map[string]any{"id": record.Id, "created": record.Created, "updated": record.Updated}
		for k, v := range record.Data {
			item[k] = v
		}
		return c.JSON(http.StatusOK, item)
	})

	// Create record
	e.POST("/api/collections/:collection/records", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}
		table := c.Param("collection")
		var data map[string]any
		if err := json.NewDecoder(c.Request().Body).Decode(&data); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid JSON")
		}
		record := lorgdb.NewRecord(table)
		if id, ok := data["id"].(string); ok && id != "" {
			record.Id = id
			delete(data, "id")
		}
		record.Load(data)
		if err := backend.DB.SaveRecord(record); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		// Fire hooks for specific collections
		backend.afterRecordWrite(table, record)
		item := map[string]any{"id": record.Id, "created": record.Created, "updated": record.Updated}
		for k, v := range record.Data {
			item[k] = v
		}
		return c.JSON(http.StatusOK, item)
	})

	// Update record
	e.PATCH("/api/collections/:collection/records/:id", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}
		table := c.Param("collection")
		id := c.Param("id")
		record, err := backend.DB.FindRecordById(table, id)
		if err != nil {
			return echo.NewHTTPError(http.StatusNotFound, "record not found")
		}
		var data map[string]any
		if err := json.NewDecoder(c.Request().Body).Decode(&data); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid JSON")
		}
		delete(data, "id")
		record.Load(data)
		if err := backend.DB.SaveRecord(record); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		// Fire hooks for specific collections
		backend.afterRecordWrite(table, record)
		item := map[string]any{"id": record.Id, "created": record.Created, "updated": record.Updated}
		for k, v := range record.Data {
			item[k] = v
		}
		return c.JSON(http.StatusOK, item)
	})

	// Delete record
	e.DELETE("/api/collections/:collection/records/:id", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}
		table := c.Param("collection")
		id := c.Param("id")
		if err := backend.DB.DeleteRecord(table, id); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		return c.NoContent(http.StatusNoContent)
	})
}

// afterRecordWrite fires inline hooks when specific collections are modified,
// replacing PocketBase's OnRecordAfterUpdateRequest/OnRecordAfterCreateRequest.
func (backend *Backend) afterRecordWrite(table string, record *lorgdb.Record) {
	switch table {
	case "_proxies":
		// Intercept toggle hook
		proxyDBID := record.Id
		intercept := record.GetBool("intercept")
		log.Printf("[InterceptManager] Proxy %s intercept changed to: %v", proxyDBID, intercept)
		ProxyMgr.mu.RLock()
		inst := ProxyMgr.instances[proxyDBID]
		ProxyMgr.mu.RUnlock()
		if inst != nil && inst.Proxy != nil {
			inst.Proxy.Intercept = intercept
		}
	case "_ui":
		// Filter update hook
		uniqueID := record.GetString("unique_id")
		if len(uniqueID) >= 7 && uniqueID[:6] == "proxy/" {
			proxyDBID := uniqueID[6:]
			ProxyMgr.mu.RLock()
			inst := ProxyMgr.instances[proxyDBID]
			ProxyMgr.mu.RUnlock()
			if inst != nil && inst.Proxy != nil {
				if filterStr, err := backend.loadProxyFilters(proxyDBID); err == nil {
					inst.Proxy.Filters = filterStr
				}
			}
		}
	}
}

// EnsureTrafficIndexesDirect creates indexes on the _data table directly.
func (backend *Backend) EnsureTrafficIndexesDirect() {
	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_data_host ON _data (host)",
		"CREATE INDEX IF NOT EXISTS idx_data_index ON _data (\"index\")",
		"CREATE INDEX IF NOT EXISTS idx_data_proxy_id ON _data (proxy_id)",
	}
	for _, idx := range indexes {
		if _, err := backend.DB.Exec(idx); err != nil {
			log.Printf("[Startup] Error creating index: %v", err)
		}
	}
}
