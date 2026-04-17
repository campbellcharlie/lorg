package launcher

import (
	"net/http"

	"github.com/campbellcharlie/lorg/lrx/version"
	"github.com/labstack/echo/v4"
)

func (launcher *Launcher) Version(e *echo.Echo) {
	e.GET("/api/version", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]interface{}{
			"version": version.RELEASED_APP_VERSION,
		})
	})
}
