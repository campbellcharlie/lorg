package tools

import (
	"github.com/campbellcharlie/lorg/internal/config"
	"github.com/campbellcharlie/lorg/internal/process"
	"github.com/campbellcharlie/lorg/internal/sdk"
	"github.com/glitchedgitz/pocketbase"
)

type Tools struct {
	App        *pocketbase.PocketBase
	Config     *config.Config
	CmdChannel chan process.RunCommandData

	// SDK client to connect to main app's database
	AppSDK *sdk.Client
	AppURL string // Main app URL (e.g., "http://localhost:8090")
}
