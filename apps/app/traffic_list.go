package app

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
)

// TrafficListItem is a lightweight row returned by /api/traffic/list
type TrafficListItem struct {
	ID          string          `json:"id"`
	Index       int             `json:"index"`
	Project     string          `json:"project"`
	Host        string          `json:"host"`
	Port        string          `json:"port"`
	IsHTTPS     bool            `json:"is_https"`
	HasResp     bool            `json:"has_resp"`
	GeneratedBy string          `json:"generated_by"`
	ReqJSON     json.RawMessage `json:"req_json"`
	RespJSON    json.RawMessage `json:"resp_json"`
	Created     string          `json:"created"`
}

// TrafficList registers GET /api/traffic/list -- a fast, direct-SQL endpoint
// that bypasses PocketBase's generic records API for performance.
// Supports ?perPage, ?page, ?host, and ?project filters.
//
// When the active projectDB has rows in its http_traffic table (the
// burp-mcp-enhanced compat schema), prefer that — that's what the
// project switcher in the UI loaded. Falls back to the lorgdb _data
// table when no projectDB is loaded or it's empty.
func (backend *Backend) TrafficList(e *echo.Echo) {
	e.GET("/api/traffic/list", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}
		// Prefer the active projectDB if it has data — this is what the
		// user just clicked in the dropdown.
		if served, ok := tryServeProjectDBTraffic(c); ok {
			return served
		}
		return servePocketBaseTraffic(c, backend)
	})
}

// tryServeProjectDBTraffic returns (response, true) when an active
// projectDB has http_traffic rows that should be shown. Returns
// (nil, false) when there's no projectDB, it's not ready, or it's
// empty — in which case the caller should fall back to lorgdb.
func tryServeProjectDBTraffic(c echo.Context) (error, bool) {
	if projectDB == nil {
		return nil, false
	}
	projectDB.mu.Lock()
	db := projectDB.db
	ready := projectDB.ready
	currentName := projectDB.name
	projectDB.mu.Unlock()
	if db == nil || !ready {
		return nil, false
	}

	var rowCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM http_traffic").Scan(&rowCount); err != nil {
		return nil, false
	}
	if rowCount == 0 {
		return nil, false
	}

	perPage, page, hostFilter := parseTrafficParams(c)
	offset := (page - 1) * perPage

	var conditions []string
	var args []any
	if hostFilter != "" {
		conditions = append(conditions, "host LIKE ?")
		args = append(args, "%"+hostFilter+"%")
	}
	whereClause := ""
	if len(conditions) > 0 {
		whereClause = " WHERE " + strings.Join(conditions, " AND ")
	}

	var totalItems int
	countArgs := append([]any{}, args...)
	if err := db.QueryRow("SELECT COUNT(*) FROM http_traffic"+whereClause, countArgs...).Scan(&totalItems); err != nil {
		totalItems = rowCount
	}

	q := `SELECT request_id, COALESCE(host,''), COALESCE(method,''), COALESCE(path,''),
	             COALESCE(query,''), COALESCE(status_code,0), COALESCE(response_length,0),
	             COALESCE(mime_type,''), COALESCE(extension,''), COALESCE(tool,''),
	             COALESCE(timestamp,''), COALESCE(protocol,''), COALESCE(port,0)
	      FROM http_traffic` + whereClause + `
	      ORDER BY request_id DESC LIMIT ? OFFSET ?`
	rowArgs := append(append([]any{}, args...), perPage, offset)

	rows, err := db.Query(q, rowArgs...)
	if err != nil {
		return nil, false
	}
	defer rows.Close()

	items := make([]TrafficListItem, 0, perPage)
	for rows.Next() {
		var (
			id             int64
			host, method   string
			path, query    string
			status         int
			respLen        int64
			mime, ext      string
			tool, ts       string
			protocol       string
			port           int
		)
		if err := rows.Scan(&id, &host, &method, &path, &query, &status, &respLen, &mime, &ext, &tool, &ts, &protocol, &port); err != nil {
			continue
		}

		// Map http_traffic row → TrafficListItem with synthesized
		// req_json / resp_json so the UI's existing renderer keeps
		// working without UI-side changes.
		req := map[string]any{"method": method, "path": path, "query": query, "ext": ext}
		resp := map[string]any{"status": status, "length": respLen, "mime": mime}
		reqB, _ := json.Marshal(req)
		respB, _ := json.Marshal(resp)

		items = append(items, TrafficListItem{
			ID:          fmt.Sprintf("%d", id),
			Index:       int(id),
			Project:     currentName,
			Host:        host,
			Port:        fmt.Sprintf("%d", port),
			IsHTTPS:     protocol == "https",
			HasResp:     status > 0,
			GeneratedBy: tool,
			ReqJSON:     reqB,
			RespJSON:    respB,
			Created:     ts,
		})
	}

	return c.JSON(http.StatusOK, trafficResponse(items, page, perPage, totalItems)), true
}

// servePocketBaseTraffic queries the PocketBase _data collection.
func servePocketBaseTraffic(c echo.Context, backend *Backend) error {
	perPage, page, hostFilter := parseTrafficParams(c)
	projectFilter := c.QueryParam("project")
	offset := (page - 1) * perPage

	// Build WHERE clause with positional params
	var conditions []string
	var args []any
	if hostFilter != "" {
		conditions = append(conditions, `host LIKE ?`)
		args = append(args, "%"+hostFilter+"%")
	}
	if projectFilter != "" {
		conditions = append(conditions, `project = ?`)
		args = append(args, projectFilter)
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = " WHERE " + strings.Join(conditions, " AND ")
	}

	var totalItems int
	countArgs := append([]any{}, args...)
	if err := backend.DB.QueryRow(`SELECT COUNT(*) FROM _data`+whereClause, countArgs...).Scan(&totalItems); err != nil {
		log.Printf("[TrafficList] Count error: %v", err)
		totalItems = 0
	}

	selectQuery := `SELECT id, "index", COALESCE(project,'') as project, host, port, is_https, has_resp, generated_by, req_json, resp_json, created
		FROM _data` + whereClause + ` ORDER BY "index" DESC LIMIT ? OFFSET ?`

	selectArgs := append(append([]any{}, args...), perPage, offset)

	var items []TrafficListItem
	rows, err := backend.DB.Query(selectQuery, selectArgs...)
	if err != nil {
		log.Printf("[TrafficList] Query error: %v", err)
		return c.JSON(http.StatusOK, emptyTrafficResponse(page, perPage))
	}
	defer rows.Close()

	for rows.Next() {
		var item TrafficListItem
		var reqJSON, respJSON *string
		if err := rows.Scan(
			&item.ID, &item.Index, &item.Project, &item.Host, &item.Port,
			&item.IsHTTPS, &item.HasResp, &item.GeneratedBy,
			&reqJSON, &respJSON, &item.Created,
		); err != nil {
			log.Printf("[TrafficList] Scan error: %v", err)
			continue
		}
		if reqJSON != nil {
			item.ReqJSON = json.RawMessage(*reqJSON)
		} else {
			item.ReqJSON = json.RawMessage("null")
		}
		if respJSON != nil {
			item.RespJSON = json.RawMessage(*respJSON)
		} else {
			item.RespJSON = json.RawMessage("null")
		}
		items = append(items, item)
	}

	return c.JSON(http.StatusOK, trafficResponse(items, page, perPage, totalItems))
}

func parseTrafficParams(c echo.Context) (perPage, page int, hostFilter string) {
	perPage = 500
	if v := c.QueryParam("perPage"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 2000 {
			perPage = n
		}
	}
	page = 1
	if v := c.QueryParam("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			page = n
		}
	}
	hostFilter = c.QueryParam("host")
	return
}

func emptyTrafficResponse(page, perPage int) map[string]interface{} {
	return map[string]interface{}{
		"page": page, "perPage": perPage,
		"totalItems": 0, "totalPages": 0,
		"items": []TrafficListItem{},
	}
}

func trafficResponse(items []TrafficListItem, page, perPage, totalItems int) map[string]interface{} {
	if items == nil {
		items = []TrafficListItem{}
	}
	totalPages := totalItems / perPage
	if totalItems%perPage != 0 {
		totalPages++
	}
	return map[string]interface{}{
		"page": page, "perPage": perPage,
		"totalItems": totalItems, "totalPages": totalPages,
		"items": items,
	}
}

// ensureProjectColumn adds the "project" TEXT column to a table if it doesn't
// already exist. This upgrades existing databases that were created before
// project tagging was added.
func ensureProjectColumn(backend *Backend, tableName string) {
	// ALTER TABLE ADD COLUMN is idempotent in practice: if the column already
	// exists, SQLite returns a "duplicate column" error which we ignore.
	if _, err := backend.DB.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN project TEXT DEFAULT ''`, tableName)); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			log.Printf("[EnsureProjectColumn] ALTER TABLE %s error: %v", tableName, err)
		}
	}
}
