package app

import (
	"net/http"
	"path"

	"github.com/campbellcharlie/lorg/lrx/version"
	"github.com/labstack/echo/v4"
)

func (backend *Backend) Info(e *echo.Echo) {
	e.GET("/api/info", func(c echo.Context) error {
		if err := requireAuth(c); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]interface{}{
			"version":    version.CURRENT_BACKEND_VERSION,
			"cwd":        path.Join(backend.Config.ProjectsDirectory, backend.Config.ProjectID),
			"project_id": backend.Config.ProjectID,
			"cache":      backend.Config.CacheDirectory,
			"config":     backend.Config.ConfigDirectory,
			"template":   backend.Config.TemplateDirectory,
		})
	})
}
