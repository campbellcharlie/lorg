package tools

import (
	"net/http"

	"github.com/campbellcharlie/lorg/internal/config"
	"github.com/campbellcharlie/lorg/internal/lorgdb"
	"github.com/campbellcharlie/lorg/internal/process"
	"github.com/campbellcharlie/lorg/internal/sdk"
	"github.com/labstack/echo/v4"
)

type Tools struct {
	DB         *lorgdb.LorgDB
	Config     *config.Config
	CmdChannel chan process.RunCommandData

	// SDK client to connect to main app's database
	AppSDK *sdk.Client
	AppURL string // Main app URL (e.g., "http://localhost:8090")
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

// RegisterRoutes wires up all HTTP endpoints on the Echo instance.
func (backend *Tools) RegisterRoutes(e *echo.Echo) {
	backend.RunCommand(e)
	backend.LoginSDKEndpoint(e)
	backend.SDKStatus(e)
	backend.StartFuzzer(e)
	backend.StopFuzzer(e)

	// Generic collection CRUD for the tool's database
	backend.registerCollectionCRUD(e)
}
