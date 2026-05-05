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
func requireAuth(c echo.Context) error {
	return requireLocalhost(c)
}

// trustNetwork, when true, lets requireLocalhost permit non-loopback callers.
// Opt-in via -allow-lan at startup; off by default so the gate's defense-in-depth
// posture is the standard.
var trustNetwork bool

// SetTrustNetwork toggles whether non-loopback API callers are allowed.
// Wired from main when -allow-lan is set.
func SetTrustNetwork(v bool) { trustNetwork = v }

// requireLocalhost checks that the request originates from a loopback address.
// This is a defense-in-depth measure for sensitive endpoints.
func requireLocalhost(c echo.Context) error {
	if trustNetwork {
		return nil
	}
	remoteAddr := c.RealIP()
	if remoteAddr == "127.0.0.1" || remoteAddr == "::1" || remoteAddr == "localhost" {
		return nil
	}
	return echo.NewHTTPError(http.StatusForbidden, "this endpoint is only accessible from localhost")
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
