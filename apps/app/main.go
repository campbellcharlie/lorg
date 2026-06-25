package app

import (
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"sync"

	"github.com/campbellcharlie/lorg/internal/config"
	"github.com/campbellcharlie/lorg/internal/lorgdb"
	"github.com/campbellcharlie/lorg/internal/process"
	"github.com/labstack/echo/v4"
	wappalyzer "github.com/projectdiscovery/wappalyzergo"
	"github.com/rs/xid"
)

type Backend struct {
	DB             *lorgdb.LorgDB
	Config         *config.Config
	Wappalyzer     *wappalyzer.Wappalyze
	CmdChannel     chan process.RunCommandData
	CounterManager *CounterManager
	MCP            *MCP
	AuditLog       *AuditLogger

	mu          sync.Mutex
	fileWatcher *fileWatcherState
}

func (backend *Backend) Serve() {
	// Structured logging: JSON in production, text in a terminal.
	var handler slog.Handler
	if fi, _ := os.Stdout.Stat(); fi != nil && (fi.Mode()&os.ModeCharDevice) != 0 {
		handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	} else {
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	}
	slog.SetDefault(slog.New(handler))

	e := echo.New()
	e.HideBanner = true
	// SECURITY: derive client IP from the real socket peer, not spoofable
	// X-Forwarded-For / X-Real-IP headers, so loopback-only auth gates hold.
	e.IPExtractor = echo.ExtractIPDirect()

	// Consistent JSON error shape: {"error": "message"}
	setupErrorHandler(e)

	// Trace ID: attach X-Trace-Id to every request for log correlation.
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			traceID := c.Request().Header.Get("X-Trace-Id")
			if traceID == "" {
				traceID = xid.New().String()
			}
			c.Response().Header().Set("X-Trace-Id", traceID)
			c.Set("trace_id", traceID)
			return next(c)
		}
	})

	// Request logging with trace ID and duration.
	//
	// Status derivation: a middleware (e.g. requireMCPAuth) that returns
	// echo.NewHTTPError surfaces here as a non-nil err BEFORE e.HTTPErrorHandler
	// runs. At that point c.Response().Status is still the default 200, so
	// logging res.Status directly mislabels rejected requests as 200 even
	// though the wire response is the error code. Prefer the HTTPError.Code
	// when err != nil so the structured log matches what the client actually
	// received.
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			err := next(c)
			status := c.Response().Status
			if err != nil {
				if he, ok := err.(*echo.HTTPError); ok {
					status = he.Code
				} else {
					status = http.StatusInternalServerError
				}
			}
			req := c.Request()
			slog.Info("http",
				"method", req.Method,
				"path", req.URL.Path,
				"status", status,
				"trace_id", c.Get("trace_id"),
			)
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
