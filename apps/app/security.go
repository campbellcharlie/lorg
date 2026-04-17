package app

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"
)

// requireAuth checks that the request originates from localhost.
// This replaces PocketBase's admin/auth context checks.
func requireAuth(c echo.Context) error {
	return requireLocalhost(c)
}

// requireLocalhost checks that the request originates from a loopback address.
// This is a defense-in-depth measure for sensitive endpoints.
func requireLocalhost(c echo.Context) error {
	remoteAddr := c.RealIP()
	if remoteAddr == "127.0.0.1" || remoteAddr == "::1" || remoteAddr == "localhost" {
		return nil
	}
	return c.JSON(http.StatusForbidden, map[string]string{
		"error": "This endpoint is only accessible from localhost",
	})
}

// validatePathContainment ensures that the resolved path stays within the
// allowed base directory. Prevents path traversal attacks (../../etc/passwd).
func validatePathContainment(basePath, userPath string) (string, error) {
	absBase, err := filepath.Abs(basePath)
	if err != nil {
		return "", fmt.Errorf("invalid base path: %w", err)
	}

	var target string
	if filepath.IsAbs(userPath) {
		target = filepath.Clean(userPath)
	} else {
		target = filepath.Clean(filepath.Join(absBase, userPath))
	}

	if target != absBase && !strings.HasPrefix(target, absBase+string(os.PathSeparator)) {
		return "", fmt.Errorf("path traversal blocked: path escapes base directory")
	}

	return target, nil
}
