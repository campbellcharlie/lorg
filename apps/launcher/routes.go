package launcher

import (
	"github.com/labstack/echo/v4"
)

// RegisterRoutes registers all launcher API routes on the Echo instance.
func (launcher *Launcher) RegisterRoutes(e *echo.Echo) {
	// Projects
	launcher.API_ListProjects(e)
	launcher.API_CreateNewProject(e)
	launcher.API_OpenProject(e)

	// Commands
	launcher.RunCommand(e)

	// SQL
	launcher.TextSQL(e)

	// Files
	launcher.SaveFile(e)
	launcher.ReadFile(e)

	// Cert
	launcher.DownloadCert(e)

	// Cook (stub)
	launcher.CookSearch(e)

	// Regex
	launcher.SearchRegex(e)

	// FileWatcher
	launcher.FileWatcher(e)

	// Templates
	launcher.TemplatesList(e)
	launcher.TemplatesNew(e)
	launcher.TemplatesDelete(e)

	// Tools
	launcher.Tools(e)
	launcher.ToolsServer(e)

	// Update
	launcher.API_CheckUpdate(e)
	launcher.API_DoUpdate(e)

	// Version
	launcher.Version(e)

	// Frontend (must be last -- catch-all)
	launcher.BindFrontend(e)
}
