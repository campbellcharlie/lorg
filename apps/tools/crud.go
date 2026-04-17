package tools

import (
	"encoding/json"
	"net/http"

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
