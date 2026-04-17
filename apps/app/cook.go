package app

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// Cook endpoints have been removed. These stubs remain so that route
// registrations in serve.go compile without changes.

func (backend *Backend) CookGenerate(e *echo.Echo) {
	e.POST("/api/cook/generate", func(c echo.Context) error {
		return c.JSON(http.StatusGone, map[string]string{"error": "cook support has been removed"})
	})
}

func (backend *Backend) CookApplyMethods(e *echo.Echo) {
	e.POST("/api/cook/apply", func(c echo.Context) error {
		return c.JSON(http.StatusGone, map[string]string{"error": "cook support has been removed"})
	})
}

func (backend *Backend) CookSearch(e *echo.Echo) {
	e.POST("/api/cook/search", func(c echo.Context) error {
		return c.JSON(http.StatusGone, map[string]string{"error": "cook support has been removed"})
	})
}
