package main

import (
	"log"
	"os"
	"path/filepath"
	"time"

	// "github.com/pocketbase/dbx"

	"github.com/campbellcharlie/lorg/apps/app"
	"github.com/campbellcharlie/lorg/internal/process"
	"github.com/campbellcharlie/lorg/internal/utils"
	"github.com/glitchedgitz/pocketbase"
	"github.com/glitchedgitz/pocketbase/core"
	"github.com/glitchedgitz/pocketbase/plugins/migratecmd"
	wappalyzer "github.com/projectdiscovery/wappalyzergo"

	_ "github.com/campbellcharlie/lorg/cmd/lorg/migrations"
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

	// Create an instance of the app structure
	API = app.Backend{
		App: pocketbase.NewWithConfig(
			pocketbase.Config{
				ProjectDir:      projectPath,
				DefaultDataDir:  "lorg",
				HideStartBanner: true,
				// DefaultDev: true,
				// DefaultEncryptionEnv: "hJH#GRJ#HG$JH$54h5kjhHJG#JHG#*&Y&EG#F&GIG@JKGH$JHRGJ##JKJH#JHG",
			},
		),
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

	// Randomize admin password on every start for security
	API.App.OnBeforeServe().Add(func(e *core.ServeEvent) error {
		admin, err := API.App.Dao().FindAdminByEmail("new@example.com")
		if err != nil {
			return nil // no default admin found, skip
		}
		pw := utils.RandomString(24)
		if err := admin.SetPassword(pw); err != nil {
			log.Printf("[Security] Failed to set admin password: %v", err)
			return nil
		}
		if err := API.App.Dao().SaveAdmin(admin); err != nil {
			log.Printf("[Security] Failed to save admin password: %v", err)
			return nil
		}
		log.Printf("[Security] Admin password randomized: %s", pw)
		return nil
	})

	migratecmd.MustRegister(API.App, API.App.RootCmd, migratecmd.Config{})

	API.App.OnBeforeServe().Add(func(e *core.ServeEvent) error {
		record, err := API.App.Dao().FindRecordById("_settings", "PROXY__________")
		if err != nil {
			log.Println("Error finding record: ", err)
			return nil
		}

		record.Set("value", "")
		if err := API.App.Dao().SaveRecord(record); err != nil {
			log.Println("Error saving record: ", err)
		}
		return nil
	})

	// Adding custom endpoints

	// Info
	API.App.OnBeforeServe().Add(API.Info)
	API.App.OnBeforeServe().Add(API.CWDContent)
	API.App.OnBeforeServe().Add(API.CWDBrowse)
	API.App.OnBeforeServe().Add(API.CWDReadFile)

	// Labels
	API.App.OnBeforeServe().Add(API.LabelAttach)
	API.App.OnBeforeServe().Add(API.LabelDelete)
	API.App.OnBeforeServe().Add(API.LabelNew)

	// Load the frontend
	API.App.OnBeforeServe().Add(API.BindFrontend)

	// Sitemap
	API.App.OnBeforeServe().Add(API.SitemapNew)
	API.App.OnBeforeServe().Add(API.SitemapFetch)

	// Send Raw Request
	API.App.OnBeforeServe().Add(API.SendRawRequest)
	API.App.OnBeforeServe().Add(API.SendHttpRaw)

	// Testing
	API.App.OnBeforeServe().Add(API.TextSQL)

	// File Operations
	API.App.OnBeforeServe().Add(API.SaveFile)
	API.App.OnBeforeServe().Add(API.ReadFile)

	// System
	API.App.OnBeforeServe().Add(API.DownloadCert)
	API.App.OnBeforeServe().Add(API.SearchRegex)
	API.App.OnBeforeServe().Add(API.FileWatcher)

	// Template
	API.App.OnBeforeServe().Add(API.TemplatesList)
	API.App.OnBeforeServe().Add(API.TemplatesNew)
	API.App.OnBeforeServe().Add(API.TemplatesDelete)

	// Commands
	API.App.OnBeforeServe().Add(API.RunCommand)
	API.App.OnBeforeServe().Add(API.Tools)

	// Cook (removed -- stub endpoints return 410 Gone)
	API.App.OnBeforeServe().Add(API.CookSearch)
	API.App.OnBeforeServe().Add(API.CookApplyMethods)
	API.App.OnBeforeServe().Add(API.CookGenerate)

	// Playground
	API.App.OnBeforeServe().Add(API.PlaygroundNew)
	API.App.OnBeforeServe().Add(API.PlaygroundDelete)
	API.App.OnBeforeServe().Add(API.PlaygroundAddChild)

	// Proxies
	API.App.OnBeforeServe().Add(API.StartProxy)
	API.App.OnBeforeServe().Add(API.StopProxy)
	API.App.OnBeforeServe().Add(API.RestartProxy)
	API.App.OnBeforeServe().Add(API.ListProxies)
	API.App.OnBeforeServe().Add(API.ScreenshotProxy)
	API.App.OnBeforeServe().Add(API.ClickProxy)
	API.App.OnBeforeServe().Add(API.GetElementsProxy)
	API.App.OnBeforeServe().Add(API.ListChromeTabs)
	API.App.OnBeforeServe().Add(API.OpenChromeTab)
	API.App.OnBeforeServe().Add(API.NavigateChromeTab)
	API.App.OnBeforeServe().Add(API.ActivateTab)
	API.App.OnBeforeServe().Add(API.CloseTab)
	API.App.OnBeforeServe().Add(API.ReloadTab)
	API.App.OnBeforeServe().Add(API.GoBack)
	API.App.OnBeforeServe().Add(API.GoForward)
	API.App.OnBeforeServe().Add(API.TypeTextProxy)
	API.App.OnBeforeServe().Add(API.WaitForSelectorProxy)
	API.App.OnBeforeServe().Add(API.EvaluateProxy)

	// Other
	API.App.OnBeforeServe().Add(API.AddRequest)
	API.App.OnBeforeServe().Add(API.InterceptEndpoints)
	API.App.OnBeforeServe().Add(API.FiltersCheck)

	// Repeater
	API.App.OnBeforeServe().Add(API.SendRepeater)

	// Traffic list (fast direct-SQL endpoint)
	API.App.OnBeforeServe().Add(API.TrafficList)

	// Traffic detail (unified endpoint)
	API.App.OnBeforeServe().Add(API.TrafficDetail)

	// Scope REST endpoints
	API.App.OnBeforeServe().Add(API.ScopeEndpoints)

	// Modify
	API.App.OnBeforeServe().Add(API.ModifyRequest)

	// Parse
	API.App.OnBeforeServe().Add(API.ParseRaw)

	// Extractor
	API.App.OnBeforeServe().Add(API.ExtractDataEndpoint)

	// Project management
	API.App.OnBeforeServe().Add(API.ProjectEndpoints)

	// MCP
	API.App.OnBeforeServe().Add(API.MCPEndpoint)

	// Xterm (Terminal) - only if explicitly enabled
	if conf.EnableTerminal {
		API.RegisterXtermRoutes()
	} else {
		log.Println("[Security] Terminal routes disabled (use --enable-terminal to enable)")
	}

	// Ensure traffic indexes exist (fixes old databases missing them)
	API.App.OnBeforeServe().Add(API.EnsureTrafficIndexes)

	API.App.OnBeforeServe().Add(func(e *core.ServeEvent) error {
		return API.InitializeProxy()
	})

	// Auto-start a headless proxy on port 9090
	API.App.OnBeforeServe().Add(func(e *core.ServeEvent) error {
		go func() {
			// Small delay to let the server finish starting
			time.Sleep(1 * time.Second)
			result, err := API.AutoStartProxy("9090")
			if err != nil {
				log.Printf("[Startup] Failed to auto-start proxy: %v", err)
			} else {
				log.Printf("[Startup] Proxy auto-started: %v", result)
			}
		}()
		return nil
	})

	API.App.OnBeforeServe().Add(func(e *core.ServeEvent) error {
		API.App.Dao().DB().NewQuery(`
			DELETE FROM _intercept;
		`).Execute()
		return nil
	})

	// Reset all proxy states and intercept settings during boot up
	API.App.OnBeforeServe().Add(func(e *core.ServeEvent) error {
		log.Println("[Startup] Resetting all proxy states and intercept settings...")

		dao := API.App.Dao()

		// Fetch all proxy records
		proxyRecords, err := dao.FindRecordsByExpr("_proxies")
		if err != nil {
			log.Printf("[Startup] Error fetching proxy records: %v", err)
			return nil
		}

		// Reset intercept to false and state to "" for each proxy
		for _, proxyRecord := range proxyRecords {
			proxyRecord.Set("intercept", false)
			proxyRecord.Set("state", "")

			if err := dao.SaveRecord(proxyRecord); err != nil {
				log.Printf("[Startup] Error updating proxy %s: %v", proxyRecord.Id, err)
			} else {
				log.Printf("[Startup] Reset proxy %s: intercept=false, state=''", proxyRecord.Id)
			}
		}

		log.Printf("[Startup] Successfully reset %d proxy records", len(proxyRecords))
		return nil
	})

	API.App.OnBeforeServe().Add(func(e *core.ServeEvent) error {
		// Setup intercept hooks
		err := API.SetupInterceptHooks()
		if err != nil {
			log.Printf("[Startup] Error setting up intercept hooks: %v", err)
			return err
		}

		// Setup filters hook
		err = API.SetupFiltersHook()
		if err != nil {
			log.Printf("[Startup] Error setting up filters hook: %v", err)
			return err
		}

		// Setup counter manager
		err = API.SetupCounterManager()
		if err != nil {
			log.Printf("[Startup] Error setting up counter manager: %v", err)
			return err
		}

		// Start periodic sync every 1 second
		go func() {
			ticker := time.NewTicker(1 * time.Second)
			defer ticker.Stop()

			for range ticker.C {
				if err := API.CounterManager.SyncToDB(); err != nil {
					// log.Printf("[CounterManager] Periodic sync error: %v", err)
				} else {
					// log.Println("[CounterManager] Periodic sync completed")
				}
			}
		}()

		return nil
	})

	API.Serve()
}
