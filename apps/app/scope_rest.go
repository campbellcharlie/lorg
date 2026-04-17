package app

import (
	"log"
	"net/http"
	"os"

	"github.com/labstack/echo/v4"
	"gopkg.in/yaml.v2"
)

// ScopeEndpoints registers REST endpoints for scope management:
//
//	GET  /api/scope          - get current rules
//	POST /api/scope/add      - add a rule
//	POST /api/scope/remove   - remove a rule by index
//	POST /api/scope/reset    - reset all rules
//	POST /api/scope/load     - load from YAML file
//	POST /api/scope/check    - check if a URL is in scope
func (backend *Backend) ScopeEndpoints(e *echo.Echo) {

	// GET /api/scope
	e.GET("/api/scope", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}
		includes, excludes := scopeManager.GetRules()
		return c.JSON(http.StatusOK, map[string]interface{}{
			"includes":   includes,
			"excludes":   excludes,
			"totalRules": len(includes) + len(excludes),
		})
	})

	// POST /api/scope/add
	e.POST("/api/scope/add", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}
		var req struct {
			Type     string `json:"type"`
			Host     string `json:"host"`
			Protocol string `json:"protocol"`
			Port     string `json:"port"`
			Path     string `json:"path"`
			Reason   string `json:"reason"`
		}
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		}
		if req.Type != "include" && req.Type != "exclude" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "type must be 'include' or 'exclude'"})
		}
		if req.Host == "" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "host is required"})
		}
		rule := ScopeRule{
			Protocol: req.Protocol,
			Host:     req.Host,
			Port:     req.Port,
			Path:     req.Path,
			Reason:   req.Reason,
		}
		scopeManager.AddRule(req.Type, rule)
		includes, excludes := scopeManager.GetRules()
		return c.JSON(http.StatusOK, map[string]interface{}{
			"success":  true,
			"includes": includes,
			"excludes": excludes,
		})
	})

	// POST /api/scope/remove
	e.POST("/api/scope/remove", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}
		var req struct {
			Type  string `json:"type"`
			Index int    `json:"index"`
		}
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		}
		if err := scopeManager.RemoveRule(req.Type, req.Index); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		includes, excludes := scopeManager.GetRules()
		return c.JSON(http.StatusOK, map[string]interface{}{
			"success":  true,
			"includes": includes,
			"excludes": excludes,
		})
	})

	// POST /api/scope/reset
	e.POST("/api/scope/reset", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}
		scopeManager.Reset()
		return c.JSON(http.StatusOK, map[string]interface{}{
			"success": true,
			"message": "All scope rules cleared",
		})
	})

	// POST /api/scope/load
	e.POST("/api/scope/load", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}
		var req struct {
			FilePath string `json:"filePath"`
		}
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		}
		if req.FilePath == "" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "filePath is required"})
		}

		data, err := os.ReadFile(req.FilePath)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "Failed to read file: " + err.Error()})
		}

		var sf scopeFile
		if err := yaml.Unmarshal(data, &sf); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "Failed to parse YAML: " + err.Error()})
		}

		scopeManager.Reset()

		allURLs := make([]string, 0, 1+len(sf.Target.AdditionalURLs))
		if sf.Target.URL != "" {
			allURLs = append(allURLs, sf.Target.URL)
		}
		allURLs = append(allURLs, sf.Target.AdditionalURLs...)

		for _, rawURL := range allURLs {
			rule, err := parseURLToRule(rawURL)
			if err != nil {
				log.Printf("[ScopeREST] Invalid target URL: %v", err)
				continue
			}
			scopeManager.AddRule("include", rule)
		}

		for _, exc := range sf.Rules.Exclusions {
			rule := ScopeRule{
				Path:   exc.Path,
				Reason: exc.Reason,
				Host:   exc.Host,
			}
			scopeManager.AddRule("exclude", rule)
		}

		includes, excludes := scopeManager.GetRules()
		return c.JSON(http.StatusOK, map[string]interface{}{
			"success":    true,
			"includes":   includes,
			"excludes":   excludes,
			"totalRules": len(includes) + len(excludes),
		})
	})

	// POST /api/scope/check
	e.POST("/api/scope/check", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}
		var req struct {
			URL string `json:"url"`
		}
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		}
		inScope, reason := scopeManager.IsInScope(req.URL)
		return c.JSON(http.StatusOK, map[string]interface{}{
			"url":     req.URL,
			"inScope": inScope,
			"reason":  reason,
		})
	})

	log.Println("[ScopeREST] Scope REST endpoints registered")
}
