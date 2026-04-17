package app

import (
	"net/http"
	"strings"

	"github.com/campbellcharlie/lorg/internal/dadql/dadql"
	"github.com/labstack/echo/v4"
)

type FilterCheckRequest struct {
	Filter  string         `json:"filter"`
	Columns map[string]any `json:"columns"`
}

// FiltersCheck registers the /api/filter/check endpoint.
// It evaluates the provided dadql filter against the given columns map.
func (backend *Backend) FiltersCheck(e *echo.Echo) {
	e.POST("/api/filter/check", func(c echo.Context) error {
		if err := requireAuth(c); err != nil {
			return err
		}

		var req FilterCheckRequest
		if err := c.Bind(&req); err != nil {
			return err
		}

		req.Filter = strings.TrimSpace(req.Filter)
		if req.Filter == "" {
			return c.JSON(http.StatusBadRequest, map[string]any{
				"error": "filter is required",
			})
		}

		ok, err := dadql.Filter(req.Columns, req.Filter)
		if err != nil {
			return c.JSON(http.StatusOK, map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
		}

		return c.JSON(http.StatusOK, map[string]any{
			"ok":    true,
			"match": ok,
		})
	})
}
