package app

import (
	"net/http"

	"github.com/campbellcharlie/lorg/lrx/frontend"
	"github.com/labstack/echo/v4"
)

func (backend *Backend) BindFrontend(e *echo.Echo) {
	handler := echo.WrapHandler(http.FileServer(http.FS(frontend.DistDirFS)))
	e.GET("/*", handler)
}
