package app

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/campbellcharlie/lorg/internal/lorgdb"
	"github.com/labstack/echo/v4"
)

// MCPTemplateEndpoints registers REST routes for the rich _mcp_templates
// store (the same one the trafficTemplate / template MCP tools write to).
//
//	GET    /api/mcp-templates           — list all templates
//	POST   /api/mcp-templates           — create / replace by name
//	DELETE /api/mcp-templates/:name     — delete
//	POST   /api/mcp-templates/send      — send a template by name with
//	                                      optional variable overrides; returns
//	                                      the response (and extracted value
//	                                      if the template has an extract regex)
func (backend *Backend) MCPTemplateEndpoints(e *echo.Echo) {

	e.GET("/api/mcp-templates", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}
		records, err := backend.DB.FindRecords("_mcp_templates", "1=1")
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		items := make([]map[string]any, 0, len(records))
		for _, r := range records {
			item := map[string]any{
				"id":               r.Id,
				"name":             r.GetString("name"),
				"tls":              r.GetBool("tls"),
				"host":             r.GetString("host"),
				"port":             int(r.GetFloat("port")),
				"http_version":     int(r.GetFloat("http_version")),
				"request_template": r.GetString("request_template"),
				"variables":        r.Get("variables"),
				"description":      r.GetString("description"),
				"inject_session":   r.GetBool("inject_session"),
				"json_escape_vars": r.GetBool("json_escape_vars"),
				"extract_regex":    r.GetString("extract_regex"),
				"extract_group":    int(r.GetFloat("extract_group")),
				"created":          r.Created,
				"updated":          r.Updated,
			}
			items = append(items, item)
		}
		return c.JSON(http.StatusOK, map[string]any{"templates": items})
	})

	// POST /api/mcp-templates — create or replace by name (idempotent on name).
	e.POST("/api/mcp-templates", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}
		var body struct {
			Name            string            `json:"name"`
			TLS             bool              `json:"tls"`
			Host            string            `json:"host"`
			Port            int               `json:"port"`
			HTTPVersion     int               `json:"http_version"`
			RequestTemplate string            `json:"request_template"`
			Variables       map[string]string `json:"variables"`
			Description     string            `json:"description"`
			InjectSession   bool              `json:"inject_session"`
			JSONEscapeVars  bool              `json:"json_escape_vars"`
			ExtractRegex    string            `json:"extract_regex"`
			ExtractGroup    int               `json:"extract_group"`
		}
		if err := c.Bind(&body); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid body"})
		}
		if strings.TrimSpace(body.Name) == "" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "name is required"})
		}
		if strings.TrimSpace(body.RequestTemplate) == "" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "request_template is required"})
		}
		if body.HTTPVersion == 0 {
			body.HTTPVersion = 1
		}

		// Replace-by-name: delete any existing template with this name first.
		if existing, err := backend.DB.FindFirstRecord("_mcp_templates", "name = ?", body.Name); err == nil && existing != nil {
			_ = backend.DB.DeleteRecord("_mcp_templates", existing.Id)
		}

		rec := lorgdb.NewRecord("_mcp_templates")
		rec.Set("name", body.Name)
		rec.Set("tls", body.TLS)
		rec.Set("host", body.Host)
		rec.Set("port", body.Port)
		rec.Set("http_version", body.HTTPVersion)
		rec.Set("request_template", body.RequestTemplate)
		if body.Variables != nil {
			rec.Set("variables", body.Variables)
		}
		rec.Set("description", body.Description)
		rec.Set("inject_session", body.InjectSession)
		rec.Set("json_escape_vars", body.JSONEscapeVars)
		rec.Set("extract_regex", body.ExtractRegex)
		rec.Set("extract_group", body.ExtractGroup)

		if err := backend.DB.SaveRecord(rec); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]any{"success": true, "id": rec.Id, "name": body.Name})
	})

	e.DELETE("/api/mcp-templates/:name", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}
		name := c.Param("name")
		rec, err := backend.DB.FindFirstRecord("_mcp_templates", "name = ?", name)
		if err != nil || rec == nil {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "template not found"})
		}
		if err := backend.DB.DeleteRecord("_mcp_templates", rec.Id); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]any{"success": true, "deleted": name})
	})

	// POST /api/mcp-templates/send — fire a template with override vars.
	// Wraps the same executeTemplate logic the MCP tool uses.
	e.POST("/api/mcp-templates/send", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}
		var body struct {
			Name      string            `json:"name"`
			Variables map[string]string `json:"variables"`
			Note      string            `json:"note"`
		}
		if err := c.Bind(&body); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid body"})
		}
		rec, err := backend.DB.FindFirstRecord("_mcp_templates", "name = ?", body.Name)
		if err != nil || rec == nil {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "template not found: " + body.Name})
		}
		resp, extracted, err := backend.executeTemplate(rec, body.Variables, body.Note)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]any{
			"success":   true,
			"response":  resp.Response,
			"time":      resp.Time,
			"extracted": extracted,
		})
	})

	// Quick helper: for POSTing arbitrary JSON validation purposes
	_ = json.Unmarshal
}
