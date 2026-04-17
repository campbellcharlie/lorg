package launcher

import (
	"fmt"
	"net/http"
	"runtime"

	"github.com/campbellcharlie/lorg/internal/updater"
	"github.com/campbellcharlie/lorg/lrx/version"
	"github.com/labstack/echo/v4"
)

func (launcher *Launcher) API_CheckUpdate(e *echo.Echo) {
	e.GET("/api/update/check", func(c echo.Context) error {
		token := updater.GetToken()

		release, err := updater.CheckLatestVersion(token)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{
				"error": fmt.Sprintf("failed to check for updates: %v", err),
			})
		}

		current := version.CURRENT_BACKEND_VERSION
		latest := release.TagName
		needsUpdate := updater.NeedsUpdate(current, latest)

		return c.JSON(http.StatusOK, map[string]interface{}{
			"current_version":  current,
			"latest_version":   latest,
			"update_available": needsUpdate,
			"platform":         runtime.GOOS + "/" + runtime.GOARCH,
		})
	})
}

func (launcher *Launcher) API_DoUpdate(e *echo.Echo) {
	e.POST("/api/update", func(c echo.Context) error {
		token := updater.GetToken()

		release, err := updater.CheckLatestVersion(token)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{
				"error": fmt.Sprintf("failed to check for updates: %v", err),
			})
		}

		current := version.CURRENT_BACKEND_VERSION
		if !updater.NeedsUpdate(current, release.TagName) {
			return c.JSON(http.StatusOK, map[string]interface{}{
				"message": "already up to date",
				"version": current,
			})
		}

		allBinaries := []string{"lorg", "lorg", "lorg-tool"}
		results := make([]map[string]string, 0, len(allBinaries))

		for _, name := range allBinaries {
			if binPath, err := updater.FindBinaryPath(name); err == nil {
				updater.CleanupOldBinaries(binPath)
			}

			asset, err := updater.FindAsset(release, name)
			if err != nil {
				results = append(results, map[string]string{
					"binary": name,
					"status": "skipped",
					"error":  err.Error(),
				})
				continue
			}

			binPath, err := updater.FindBinaryPath(name)
			if err != nil {
				results = append(results, map[string]string{
					"binary": name,
					"status": "skipped",
					"error":  err.Error(),
				})
				continue
			}

			if err := updater.UpdateBinary(asset.URL, binPath, token); err != nil {
				results = append(results, map[string]string{
					"binary": name,
					"status": "failed",
					"error":  err.Error(),
				})
				continue
			}

			results = append(results, map[string]string{
				"binary": name,
				"status": "updated",
				"path":   binPath,
			})
		}

		return c.JSON(http.StatusOK, map[string]interface{}{
			"previous_version": current,
			"new_version":      release.TagName,
			"results":          results,
		})
	})
}
