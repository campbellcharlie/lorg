package launcher

import (
	"net/http"

	"github.com/glitchedgitz/pocketbase/core"
	"github.com/labstack/echo/v5"
)

// Cook endpoints have been removed. This stub remains so that route
// registrations in cmd/lorg-launcher/main.go compile without changes.

func (launcher *Launcher) CookSearch(e *core.ServeEvent) error {
	e.Router.AddRoute(echo.Route{
		Method: "POST",
		Path:   "/api/cook/search",
		Handler: func(c echo.Context) error {
			return c.JSON(http.StatusGone, map[string]string{"error": "cook support has been removed"})
		},
	})
	return nil
}
