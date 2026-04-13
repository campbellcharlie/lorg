package app

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/glitchedgitz/pocketbase/apis"
	"github.com/glitchedgitz/pocketbase/core"
	"github.com/glitchedgitz/pocketbase/models/schema"
	"github.com/labstack/echo/v5"
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

// TrafficList registers GET /api/traffic/list — a fast, direct-SQL endpoint
// that bypasses PocketBase's generic records API for performance.
// Supports ?perPage, ?page, ?host, and ?project filters.
func (backend *Backend) TrafficList(e *core.ServeEvent) error {
	e.Router.AddRoute(echo.Route{
		Method: http.MethodGet,
		Path:   "/api/traffic/list",
		Handler: func(c echo.Context) error {
			if err := requireLocalhost(c); err != nil {
				return err
			}
			return servePocketBaseTraffic(c, backend)
		},
		Middlewares: []echo.MiddlewareFunc{
			apis.ActivityLogger(backend.App),
		},
	})
	return nil
}

// servePocketBaseTraffic queries the PocketBase _data collection.
func servePocketBaseTraffic(c echo.Context, backend *Backend) error {
	perPage, page, hostFilter := parseTrafficParams(c)
	projectFilter := c.QueryParam("project")
	offset := (page - 1) * perPage

	db := backend.App.Dao().DB()

	// Build WHERE clause
	var conditions []string
	binds := map[string]interface{}{"limit": perPage, "offset": offset}
	if hostFilter != "" {
		conditions = append(conditions, `host LIKE {:host}`)
		binds["host"] = "%" + hostFilter + "%"
	}
	if projectFilter != "" {
		conditions = append(conditions, `project = {:project}`)
		binds["project"] = projectFilter
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = " WHERE " + strings.Join(conditions, " AND ")
	}

	var totalItems int
	q := db.NewQuery(`SELECT COUNT(*) FROM _data` + whereClause)
	q.Bind(binds)
	if err := q.Row(&totalItems); err != nil {
		log.Printf("[TrafficList] Count error: %v", err)
		totalItems = 0
	}

	selectQuery := `SELECT id, "index", COALESCE(project,'') as project, host, port, is_https, has_resp, generated_by, req_json, resp_json, created
		FROM _data` + whereClause + ` ORDER BY "index" DESC LIMIT {:limit} OFFSET {:offset}`

	q2 := db.NewQuery(selectQuery)
	q2.Bind(binds)

	var items []TrafficListItem
	rows, err := q2.Rows()
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

// EnsureTrafficIndexes creates indexes and adds missing columns on the _data table.
func (backend *Backend) EnsureTrafficIndexes(e *core.ServeEvent) error {
	db := backend.App.Dao().DB()

	// Ensure project field exists on _data and _proxies collections (for existing databases)
	ensureProjectField(backend, "_data")
	ensureProjectField(backend, "_proxies")

	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_data_index ON _data ("index" DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_data_host ON _data (host)`,
		`CREATE INDEX IF NOT EXISTS idx_data_generated_by ON _data (generated_by)`,
		`CREATE INDEX IF NOT EXISTS idx_data_project ON _data (project)`,
	}

	for _, idx := range indexes {
		if _, err := db.NewQuery(idx).Execute(); err != nil {
			log.Printf("[EnsureTrafficIndexes] Error creating index: %v", err)
		}
	}

	log.Println("[Startup] Traffic indexes ensured")
	return nil
}

// ensureProjectField adds the "project" text field to a PocketBase collection
// if it doesn't already exist. This upgrades existing databases that were
// created before project tagging was added.
func ensureProjectField(backend *Backend, collectionName string) {
	dao := backend.App.Dao()
	db := dao.DB()
	collection, err := dao.FindCollectionByNameOrId(collectionName)
	if err != nil {
		return
	}

	// Check if field already exists in PocketBase schema
	for _, f := range collection.Schema.Fields() {
		if f.Name == "project" {
			return // already exists in schema
		}
	}

	// Ensure the SQLite column exists first (idempotent — duplicate column error is expected)
	if _, err := db.NewQuery(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN project TEXT DEFAULT ''`, collectionName)).Execute(); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			log.Printf("[EnsureProjectField] ALTER TABLE %s error: %v", collectionName, err)
		}
	}

	// Update PocketBase's schema metadata by adding the field and saving
	// the collection record directly (bypassing table sync which would fail
	// since the column already exists).
	collection.Schema.AddField(&schema.SchemaField{
		Name: "project",
		Type: schema.FieldTypeText,
	})

	// Save the collection metadata directly to _collections table
	encodedSchema, _ := collection.Schema.MarshalJSON()
	_, err = db.NewQuery(`UPDATE _collections SET schema = {:schema} WHERE id = {:id}`).Bind(map[string]interface{}{
		"schema": string(encodedSchema),
		"id":     collection.Id,
	}).Execute()
	if err != nil {
		log.Printf("[EnsureProjectField] Error updating schema for %s: %v", collectionName, err)
	} else {
		log.Printf("[EnsureProjectField] Added project field to %s schema", collectionName)
		// Refresh the cached collection
		dao.FindCollectionByNameOrId(collectionName)
	}
}
