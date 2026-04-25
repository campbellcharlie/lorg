package main

import (
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/campbellcharlie/lorg/apps/app"
	"github.com/campbellcharlie/lorg/internal/lorgdb"
	"github.com/campbellcharlie/lorg/internal/process"
	wappalyzer "github.com/projectdiscovery/wappalyzergo"
)

func serve(projectPath string) {

	wappalyzerClient, err := wappalyzer.New()
	if err != nil {
		log.Println("Wappalyzer Error: ", err)
	}

	os.MkdirAll(projectPath, 0755)

	// Extract project ID from project path (the directory name)
	projectID := filepath.Base(projectPath)
	conf.ProjectID = projectID
	log.Printf("Project ID: %s", projectID)

	// Open LorgDB (same data.db path PocketBase used)
	dbPath := filepath.Join(projectPath, "lorg", "pb_data", "data.db")
	ldb, err := lorgdb.Open(dbPath)
	if err != nil {
		log.Fatalf("[Startup] Failed to open LorgDB: %v", err)
	}
	if err := ldb.RunMigrations(); err != nil {
		log.Fatalf("[Startup] LorgDB migrations failed: %v", err)
	}

	// Seed defaults for fresh databases
	if err := ldb.SeedDefaults(); err != nil {
		log.Printf("[Startup] Warning: seed defaults failed: %v", err)
	}

	// Create the backend
	API = app.Backend{
		DB:         ldb,
		Wappalyzer: wappalyzerClient,
		Config:     &conf,
		CmdChannel: make(chan process.RunCommandData),
	}

	// Initialize audit logger
	auditDir := filepath.Join(projectPath, "audit")
	auditLogger, auditErr := app.NewAuditLogger(auditDir)
	if auditErr != nil {
		log.Printf("[Startup] Audit logger failed to initialize: %v", auditErr)
	} else {
		API.AuditLog = auditLogger
		auditLogger.Log("server_start", map[string]string{
			"project": projectID,
			"host":    conf.HostAddr,
		})
	}

	// ----- Startup logic (was in OnBeforeServe hooks) -----

	// Reset proxy setting
	record, err := API.DB.FindRecordById("_settings", "PROXY__________")
	if err == nil {
		record.Set("value", "")
		if err := API.DB.SaveRecord(record); err != nil {
			log.Println("Error saving record: ", err)
		}
	}

	// Clean intercept table
	API.DB.Exec("DELETE FROM _intercept")

	// Reset all proxy states
	log.Println("[Startup] Resetting all proxy states and intercept settings...")
	proxyRecords, err := API.DB.FindRecords("_proxies", "1=1")
	if err == nil {
		for _, proxyRecord := range proxyRecords {
			proxyRecord.Set("intercept", false)
			proxyRecord.Set("state", "")
			if err := API.DB.SaveRecord(proxyRecord); err != nil {
				log.Printf("[Startup] Error updating proxy %s: %v", proxyRecord.Id, err)
			}
		}
		log.Printf("[Startup] Successfully reset %d proxy records", len(proxyRecords))
	}

	// Ensure traffic indexes exist
	API.EnsureTrafficIndexesDirect()

	// Initialize match & replace rules
	API.InitMatchReplace()

	// Initialize the project DB cycler with the configured directory so
	// the UI's project switcher is populated immediately on boot.
	if conf.ProjectsDBDirectory != "" {
		if err := app.InitProjectsDir(conf.ProjectsDBDirectory); err != nil {
			log.Printf("[Startup] Project DB init failed: %v", err)
		}
	}

	// Initialize proxy system
	if err := API.InitializeProxy(); err != nil {
		log.Printf("[Startup] Error initializing proxy: %v", err)
	}

	// Setup counter manager
	if err := API.SetupCounterManager(); err != nil {
		log.Printf("[Startup] Error setting up counter manager: %v", err)
	}

	// Start periodic counter sync every 1 second
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if API.CounterManager != nil {
				API.CounterManager.SyncToDB()
			}
		}
	}()

	// Auto-start a headless proxy on port 9090
	go func() {
		time.Sleep(1 * time.Second)
		result, err := API.AutoStartProxy("9090")
		if err != nil {
			log.Printf("[Startup] Failed to auto-start proxy: %v", err)
		} else {
			log.Printf("[Startup] Proxy auto-started: %v", result)
		}
	}()

	// Xterm (Terminal) - only if explicitly enabled
	if conf.EnableTerminal {
		log.Println("[Security] Terminal routes enabled")
	} else {
		log.Println("[Security] Terminal routes disabled (use --enable-terminal to enable)")
	}

	API.Serve()
}
