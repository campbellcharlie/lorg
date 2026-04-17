package app

import (
	"fmt"
	"log"
	"sync"

	"github.com/campbellcharlie/lorg/internal/config"
	"github.com/campbellcharlie/lorg/internal/lorgdb"
	"github.com/campbellcharlie/lorg/internal/process"
	"github.com/labstack/echo/v4"
	wappalyzer "github.com/projectdiscovery/wappalyzergo"
)

type Backend struct {
	DB             *lorgdb.LorgDB
	Config         *config.Config
	Wappalyzer     *wappalyzer.Wappalyze
	CmdChannel     chan process.RunCommandData
	CounterManager *CounterManager
	XtermManager   *XtermManager
	MCP            *MCP
	AuditLog       *AuditLogger

	mu          sync.Mutex
	fileWatcher *fileWatcherState
}

func (backend *Backend) Serve() {
	e := echo.New()

	// Simple request logging middleware
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			err := next(c)
			req := c.Request()
			res := c.Response()
			log.Printf("[HTTP] %s %s → %d", req.Method, req.URL.Path, res.Status)
			return err
		}
	})

	// Register all routes
	backend.RegisterRoutes(e)

	fmt.Printf(`
Application:        http://%s
API:                http://%s/api/
Cert:               http://%s/cacert.crt

	`, backend.Config.HostAddr, backend.Config.HostAddr, backend.Config.HostAddr)

	go backend.CommandManager()

	if err := e.Start(backend.Config.HostAddr); err != nil {
		log.Fatalf("[Server] %v", err)
	}
}

// CreateCollection creates a table with the given columns.
// Used by sitemap to create per-host tables dynamically.
func (backend *Backend) CreateCollection(collectionName string, columns []string) error {
	colDefs := []string{
		`id TEXT PRIMARY KEY NOT NULL`,
		`created TEXT DEFAULT ''`,
		`updated TEXT DEFAULT ''`,
	}
	colDefs = append(colDefs, columns...)

	ddl := fmt.Sprintf("CREATE TABLE IF NOT EXISTS \"%s\" (%s)",
		collectionName,
		joinStrings(colDefs, ", "),
	)
	_, err := backend.DB.Exec(ddl)
	return err
}

func joinStrings(ss []string, sep string) string {
	result := ""
	for i, s := range ss {
		if i > 0 {
			result += sep
		}
		result += s
	}
	return result
}
