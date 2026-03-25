package app

import (
	"net/http"

	"github.com/glitchedgitz/pocketbase/core"
	"github.com/labstack/echo/v5"
)

// Cook endpoints have been removed. These stubs remain so that route
// registrations in serve.go compile without changes.

func (backend *Backend) CookGenerate(e *core.ServeEvent) error {
	e.Router.AddRoute(echo.Route{
		Method: http.MethodPost,
		Path:   "/api/cook/generate",
		Handler: func(c echo.Context) error {
			return c.JSON(http.StatusGone, map[string]string{"error": "cook support has been removed"})
		},
	})
	return nil
}

func (backend *Backend) CookApplyMethods(e *core.ServeEvent) error {
	e.Router.AddRoute(echo.Route{
		Method: http.MethodPost,
		Path:   "/api/cook/apply",
		Handler: func(c echo.Context) error {
			return c.JSON(http.StatusGone, map[string]string{"error": "cook support has been removed"})
		},
	})
	return nil
}

func (backend *Backend) CookSearch(e *core.ServeEvent) error {
	e.Router.AddRoute(echo.Route{
		Method: http.MethodPost,
		Path:   "/api/cook/search",
		Handler: func(c echo.Context) error {
			return c.JSON(http.StatusGone, map[string]string{"error": "cook support has been removed"})
		},
	})
	return nil
}
