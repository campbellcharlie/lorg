package launcher

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// Cook endpoints have been removed. This stub remains so that route
// registrations compile without changes.

func (launcher *Launcher) CookSearch(e *echo.Echo) {
	e.POST("/api/cook/search", func(c echo.Context) error {
		return c.JSON(http.StatusGone, map[string]string{"error": "cook support has been removed"})
	})
}
