package tools

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/campbellcharlie/lorg/internal/lorgdb"
	"github.com/labstack/echo/v4"
)

// registerCollectionCRUD provides basic CRUD endpoints for the tool's database,
// matching the pattern in the main app's routes.go.
func (backend *Tools) registerCollectionCRUD(e *echo.Echo) {
	// List records
	e.GET("/api/collections/:collection/records", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}
		table := c.Param("collection")
		filter := c.QueryParam("filter")
		sort := c.QueryParam("sort")

		// SECURITY: validate identifiers and reject raw filter (arbitrary WHERE) -> SQLi.
		if !isSafeIdentifier(table) {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid collection name")
		}
		if filter != "" {
			return echo.NewHTTPError(http.StatusBadRequest, "filter parameter is not supported")
		}
		if sort != "" && !isSafeOrderClause(sort) {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid sort parameter")
		}

		where := "1=1"
		var args []any

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

// isSafeIdentifier reports whether s is a safe SQL identifier.
func isSafeIdentifier(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i, r := range s {
		ok := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (i > 0 && r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return true
}

// isSafeOrderClause validates a comma-separated ORDER BY list.
func isSafeOrderClause(s string) bool {
	for _, part := range strings.Split(s, ",") {
		f := strings.Fields(strings.TrimSpace(part))
		if len(f) == 0 || len(f) > 2 {
			return false
		}
		if !isSafeIdentifier(f[0]) {
			return false
		}
		if len(f) == 2 {
			if d := strings.ToUpper(f[1]); d != "ASC" && d != "DESC" {
				return false
			}
		}
	}
	return true
}
