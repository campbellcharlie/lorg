package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/campbellcharlie/lorg/apps/tools"
	"github.com/campbellcharlie/lorg/internal/config"
	"github.com/campbellcharlie/lorg/internal/lorgdb"
	"github.com/campbellcharlie/lorg/internal/process"
	"github.com/campbellcharlie/lorg/internal/utils"
	"github.com/labstack/echo/v4"
)

var conf config.Config

func initialize() {
	conf.Initiate()
}

func main() {

	initialize()

	var host string
	var path string
	var name string

	flag.StringVar(&host, "host", "127.0.0.1:8090", "Host address to listen on")
	flag.StringVar(&path, "path", ".", "Project directory path")
	flag.StringVar(&name, "name", "lorg-tool", "tool name")
	flag.Parse()

	// Resolve the project path to an absolute path
	projectPath, err := filepath.Abs(path)
	utils.CheckErr("Failed to resolve project path", err)

	// Change working directory to the project directory
	err = os.Chdir(projectPath)
	utils.CheckErr("Failed to change working directory to project path", err)

	fmt.Println("Working directory changed to:", projectPath)

	// Open lorgdb for the tool's database
	dbPath := filepath.Join(projectPath, name, "pb_data", "data.db")
	db, err := lorgdb.Open(dbPath)
	if err != nil {
		log.Fatalf("[Startup] Failed to open LorgDB: %v", err)
	}
	defer db.Close()

	if err := db.RunMigrations(); err != nil {
		log.Fatalf("[Startup] LorgDB migrations failed: %v", err)
	}

	backend := tools.Tools{
		DB:         db,
		Config:     &conf,
		CmdChannel: make(chan process.RunCommandData),
	}

	go backend.CommandManager()

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
	backend.RegisterRoutes(e)

	fmt.Printf("\nlorg-tool listening on: http://%s\n\n", host)

	if err := e.Start(host); err != nil {
		log.Fatalf("[Server] %v", err)
	}
}
