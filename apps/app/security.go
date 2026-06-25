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

// isLoopbackRequest reports whether the request's RealIP is a loopback address.
// Shared by requireLocalhost, requireLoopbackOnly, and requireMCPAuth so they
// agree on what "local" means.
func isLoopbackRequest(c echo.Context) bool {
	remoteAddr := c.RealIP()
	return remoteAddr == "127.0.0.1" || remoteAddr == "::1" || remoteAddr == "localhost"
}

// requireLocalhost checks that the request originates from a loopback address.
// This is a defense-in-depth measure for sensitive endpoints.
func requireLocalhost(c echo.Context) error {
	if trustNetwork {
		return nil
	}
	if isLoopbackRequest(c) {
		return nil
	}
	return echo.NewHTTPError(http.StatusForbidden, "this endpoint is only accessible from localhost")
}

// requireLoopbackOnly is like requireLocalhost but ignores the trustNetwork
// (-allow-lan) bypass. Used for endpoints that must never be exposed to the
// LAN even when -allow-lan is set (e.g. tool enumeration / recon surfaces).
func requireLoopbackOnly(c echo.Context) error {
	if isLoopbackRequest(c) {
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

// isSafeIdentifier reports whether s is a safe SQL identifier (table/column),
// so it can be interpolated where bind parameters are not possible.
func isSafeIdentifier(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i, r := range s {
		ok := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (i > 0 && r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return true
}

// isSafeOrderClause validates a comma-separated ORDER BY list: each term must be
// a safe identifier with an optional ASC/DESC direction.
func isSafeOrderClause(s string) bool {
	for _, part := range strings.Split(s, ",") {
		f := strings.Fields(strings.TrimSpace(part))
		if len(f) == 0 || len(f) > 2 {
			return false
		}
		if !isSafeIdentifier(f[0]) {
			return false
		}
		if len(f) == 2 {
			if d := strings.ToUpper(f[1]); d != "ASC" && d != "DESC" {
				return false
			}
		}
	}
	return true
}
