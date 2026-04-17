package launcher

import (
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/labstack/echo/v4"
)

func (launcher *Launcher) DownloadCert(e *echo.Echo) {
	e.GET("/cacert.crt", func(c echo.Context) error {
		// Certificate is always at this fixed location (generated at startup)
		certPath := filepath.Join(launcher.Config.ConfigDirectory, "ca.crt")

		// Verify certificate exists
		if _, err := os.Stat(certPath); os.IsNotExist(err) {
			log.Printf("[Certificate] ERROR: Certificate not found at %s", certPath)
			return c.JSON(http.StatusInternalServerError, map[string]string{
				"error": "Certificate not found. Please restart the application.",
			})
		}

		log.Printf("[Certificate] Serving: %s", certPath)
		return c.Attachment(certPath, "lorg-ca.crt")
	})
}
