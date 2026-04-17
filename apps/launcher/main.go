package launcher

import (
	"fmt"
	"log"

	"github.com/campbellcharlie/lorg/internal/config"
	"github.com/campbellcharlie/lorg/internal/lorgdb"
	"github.com/campbellcharlie/lorg/internal/process"
	"github.com/labstack/echo/v4"
)

type Launcher struct {
	DB         *lorgdb.LorgDB
	Config     *config.Config
	CmdChannel chan process.RunCommandData
}

func (launcher *Launcher) Serve() {
	e := echo.New()

	// Simple request logging middleware
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			err := next(c)
			req := c.Request()
			res := c.Response()
			log.Printf("[HTTP] %s %s -> %d", req.Method, req.URL.Path, res.Status)
			return err
		}
	})

	// Register all routes
	launcher.RegisterRoutes(e)

	fmt.Printf(`
Application:        http://%s
API:                http://%s/api/
Cert:               http://%s/cacert.crt

	`, launcher.Config.HostAddr, launcher.Config.HostAddr, launcher.Config.HostAddr)

	go launcher.CommandManager()

	if err := e.Start(launcher.Config.HostAddr); err != nil {
		log.Fatalf("[Server] %v", err)
	}
}
