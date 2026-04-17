package launcher

import (
	"net/http"

	"github.com/campbellcharlie/lorg/lrx/frontend"
	"github.com/labstack/echo/v4"
)

func (launcher *Launcher) BindFrontend(e *echo.Echo) {
	handler := echo.WrapHandler(http.FileServer(http.FS(frontend.DistDirFS)))
	e.GET("/*", handler, func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if c.Request().Method == http.MethodGet {
				c.Response().Header().Set("Cache-Control", "public, max-age=3600")
			}
			return next(c)
		}
	})
}
