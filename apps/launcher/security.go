package launcher

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// requireAuth checks that the request is authorized. For the launcher,
// this delegates to requireLocalhost since there are no user accounts.
func requireAuth(c echo.Context) error {
	return requireLocalhost(c)
}

// requireLocalhost checks that the request originates from a loopback address.
func requireLocalhost(c echo.Context) error {
	remoteAddr := c.RealIP()
	if remoteAddr == "127.0.0.1" || remoteAddr == "::1" || remoteAddr == "localhost" {
		return nil
	}
	return c.JSON(http.StatusForbidden, map[string]string{
		"error": "This endpoint is only accessible from localhost",
	})
}
