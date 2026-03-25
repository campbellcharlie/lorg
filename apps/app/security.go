package app

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/glitchedgitz/pocketbase/apis"
	"github.com/glitchedgitz/pocketbase/models"
	"github.com/labstack/echo/v5"
)

// requireAuth checks that the request is authenticated via PocketBase admin or
// auth record context. Returns an error response if unauthenticated.
// Usage: if err := requireAuth(c); err != nil { return err }
func requireAuth(c echo.Context) error {
	admin, _ := c.Get(apis.ContextAdminKey).(*models.Admin)
	record, _ := c.Get(apis.ContextAuthRecordKey).(*models.Record)
	if admin == nil && record == nil {
		return c.JSON(http.StatusForbidden, map[string]string{
			"error": "Authentication required",
		})
	}
	return nil
}

// requireLocalhost checks that the request originates from a loopback address.
// This is a defense-in-depth measure for sensitive endpoints.
// Usage: if err := requireLocalhost(c); err != nil { return err }
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
// Returns the cleaned absolute path or an error.
func validatePathContainment(basePath, userPath string) (string, error) {
	absBase, err := filepath.Abs(basePath)
	if err != nil {
		return "", fmt.Errorf("invalid base path: %w", err)
	}

	// If userPath is absolute, use it directly; otherwise join with base
	var target string
	if filepath.IsAbs(userPath) {
		target = filepath.Clean(userPath)
	} else {
		target = filepath.Clean(filepath.Join(absBase, userPath))
	}

	// Ensure target is within base directory
	if target != absBase && !strings.HasPrefix(target, absBase+string(os.PathSeparator)) {
		return "", fmt.Errorf("path traversal blocked: path escapes base directory")
	}

	return target, nil
}
