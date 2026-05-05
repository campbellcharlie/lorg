package app

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// errorResponse is the single JSON shape for all API errors.
type errorResponse struct {
	Error string `json:"error"`
}

// setupErrorHandler wires a custom error handler onto e that normalises
// *echo.HTTPError (returned by echo.NewHTTPError and framework middleware)
// into {"error": "message"} — the same shape used by direct c.JSON error
// responses throughout the codebase.
func setupErrorHandler(e *echo.Echo) {
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		if c.Response().Committed {
			return
		}

		code := http.StatusInternalServerError
		msg := "internal server error"

		if he, ok := err.(*echo.HTTPError); ok {
			code = he.Code
			if m, ok := he.Message.(string); ok {
				msg = m
			}
		}

		c.JSON(code, errorResponse{Error: msg}) //nolint:errcheck
	}
}
