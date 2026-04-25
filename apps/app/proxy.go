package app

import (
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"path"
	"sync"
	"sync/atomic"

	"github.com/campbellcharlie/lorg/internal/lorgdb"
	"github.com/campbellcharlie/lorg/internal/utils"
	"github.com/campbellcharlie/lorg/lrx/browser"
	"github.com/labstack/echo/v4"
)

// ProxyInstance holds a proxy and its optional runtime attachments (browser, label, etc.)
type ProxyInstance struct {
	Proxy      *RawProxyWrapper
	Browser    string // `json:"browser"`
	BrowserCmd *exec.Cmd
	Label      string // `json:"label"`
	Project    string // Project name this proxy belongs to
}

// ProxyManager manages multiple proxy instances
type ProxyManager struct {
	instances  map[string]*ProxyInstance
	mu         sync.RWMutex
	index      atomic.Uint64 // Shared atomic counter for unique indices across all proxies (for requests)
	proxyIndex atomic.Uint64 // Counter for proxy IDs
}

// ProxyMgr is the global proxy manager instance.
// Intentionally kept as a package-level var rather than a Backend field because:
// 1. It is initialized before Backend exists (used during proxy setup)
// 2. It is accessed from RawProxyWrapper callbacks which don't have Backend reference
// 3. Multiple packages reference it for request ID coordination
var ProxyMgr = &ProxyManager{
	instances: make(map[string]*ProxyInstance),
}

// init is intentionally empty - initialization happens on first proxy start
func init() {
}

// SetGlobalIndex sets the global index from the database
func (pm *ProxyManager) SetGlobalIndex(value uint64) {
	pm.index.Store(value)
	log.Printf("[ProxyManager] Global index set to: %d", value)
}

// GetNextIndex returns the next unique index (thread-safe)
func (pm *ProxyManager) GetNextIndex() uint64 {
	return pm.index.Add(1)
}

// GetNextProxyID returns the next unique proxy ID (thread-safe)
func (pm *ProxyManager) GetNextProxyID() string {
	idx := pm.proxyIndex.Add(1)
	return utils.FormatNumericID(float64(idx), 15)
}

// initializeIndexFromDB queries the database to get the current max index
func (pm *ProxyManager) initializeIndexFromDB(backend *Backend) error {
	var count int
	err := backend.DB.QueryRow("SELECT COUNT(*) FROM _data").Scan(&count)
	if err != nil {
		return fmt.Errorf("failed to query total rows: %w", err)
	}

	// Set the atomic counter to the total rows count
	totalRows := uint64(count)
	pm.index.Store(totalRows)

	log.Printf("[ProxyManager] ========================================")
	log.Printf("[ProxyManager] Global Index Initialization:")
	log.Printf("[ProxyManager]   - Total rows in database: %d", totalRows)
	log.Printf("[ProxyManager]   - Next index will be: %d", totalRows+1)
	log.Printf("[ProxyManager]   - Counter starting at: %d", totalRows)
	log.Printf("[ProxyManager] ========================================")

	return nil
}

// initializeProxyIndexFromDB queries the database to get the current max proxy count
func (pm *ProxyManager) initializeProxyIndexFromDB(backend *Backend) error {
	var count int
	err := backend.DB.QueryRow("SELECT COUNT(*) FROM _proxies").Scan(&count)
	if err != nil {
		return fmt.Errorf("failed to query total proxies: %w", err)
	}

	// Set the proxy index counter
	totalProxies := uint64(count)
	pm.proxyIndex.Store(totalProxies)

	log.Printf("[ProxyManager] Proxy Index Initialization:")
	log.Printf("[ProxyManager]   - Total proxies in database: %d", totalProxies)
	log.Printf("[ProxyManager]   - Next proxy ID will use index: %d", totalProxies+1)

	return nil
}

// GetProxy returns a proxy by ID (listen address)
func (pm *ProxyManager) GetProxy(id string) *RawProxyWrapper {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	if inst := pm.instances[id]; inst != nil {
		return inst.Proxy
	}
	return nil
}

// GetInstance returns a proxy instance by ID
func (pm *ProxyManager) GetInstance(id string) *ProxyInstance {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.instances[id]
}

// AddProxy adds a proxy to the manager
func (pm *ProxyManager) AddProxy(id string, proxy *RawProxyWrapper) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if inst := pm.instances[id]; inst != nil {
		inst.Proxy = proxy
	} else {
		pm.instances[id] = &ProxyInstance{Proxy: proxy}
	}
}

// AddProxyInstance adds a complete proxy instance to the manager
func (pm *ProxyManager) AddProxyInstance(id string, instance *ProxyInstance) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.instances[id] = instance
}

// RemoveProxy removes a proxy from the manager
func (pm *ProxyManager) RemoveProxy(id string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	delete(pm.instances, id)
}

// GetAllProxies returns all proxy IDs
func (pm *ProxyManager) GetAllProxies() []string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	ids := make([]string, 0, len(pm.instances))
	for id := range pm.instances {
		ids = append(ids, id)
	}
	return ids
}

// StopProxy stops a specific proxy
func (pm *ProxyManager) StopProxy(id string) error {
	log.Printf("[ProxyManager] StopProxy called for ID: %s", id)

	pm.mu.RLock()
	inst := pm.instances[id]
	pm.mu.RUnlock()

	if inst == nil || inst.Proxy == nil {
		log.Printf("[ProxyManager] Proxy with ID '%s' not found", id)
		return fmt.Errorf("proxy %s not found", id)
	}

	log.Printf("[ProxyManager] Proxy found, calling Stop()...")
	err := inst.Proxy.Stop()
	// attempt to close tied browser/terminal if any
	pm.mu.Lock()
	if inst.BrowserCmd != nil && inst.BrowserCmd.Process != nil {
		clientType := "browser"
		isTerminal := inst.Browser == "terminal"
		if isTerminal {
			clientType = "terminal"
		}
		log.Printf("[ProxyManager] Attempting to terminate %s for proxy %s (pid=%d)", clientType, id, inst.BrowserCmd.Process.Pid)

		var killErr error
		if isTerminal {
			// Use special terminal cleanup for better window closing
			killErr = browser.CloseTerminalWindow(inst.BrowserCmd)
		} else {
			// Standard browser process kill
			killErr = inst.BrowserCmd.Process.Kill()
		}

		if killErr != nil {
			log.Printf("[ProxyManager] Failed to kill %s process for %s: %v", clientType, id, killErr)
		} else {
			log.Printf("[ProxyManager] %s process for %s terminated", clientType, id)
		}
	}

	pm.mu.Unlock()
	return err
}

// StopAllProxies stops all running proxies and cleans up all resources
func (pm *ProxyManager) StopAllProxies() {
	pm.mu.Lock()
	ids := make([]string, 0, len(pm.instances))
	for id := range pm.instances {
		ids = append(ids, id)
	}
	pm.mu.Unlock()

	for _, id := range ids {
		if err := pm.StopProxy(id); err != nil {
			log.Printf("[ProxyManager] Error stopping proxy %s: %v", id, err)
		}
	}

	pm.mu.Lock()
	pm.instances = make(map[string]*ProxyInstance)
	pm.mu.Unlock()
}

// ApplyToAllProxies applies a function to all running proxies
func (pm *ProxyManager) ApplyToAllProxies(fn func(proxy *RawProxyWrapper, proxyID string)) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	for id, inst := range pm.instances {
		if inst != nil && inst.Proxy != nil {
			fn(inst.Proxy, id)
		}
	}
}

// DEPRECATED: Backward compatibility - returns first proxy or nil
var PROXY *RawProxyWrapper

func updateProxyVar() {
	ProxyMgr.mu.RLock()
	defer ProxyMgr.mu.RUnlock()

	// Set PROXY to first proxy for backward compatibility
	for _, inst := range ProxyMgr.instances {
		if inst != nil && inst.Proxy != nil {
			PROXY = inst.Proxy
			return
		}
	}
	PROXY = nil
}

// loadProxySettings loads intercept and filter settings for a proxy
func (backend *Backend) loadProxySettings(proxy *RawProxyWrapper, proxyRecord *lorgdb.Record) error {
	log.Printf("[ProxySettings] Loading settings for proxy ID: %s", proxyRecord.Id)

	// Load intercept setting from _proxies record
	intercept := proxyRecord.GetBool("intercept")
	proxy.Intercept = intercept
	log.Printf("[ProxySettings] Intercept: %v", intercept)

	// Load filters from _ui collection (format: proxy/{proxyID})
	filterstring, err := backend.loadProxyFilters(proxyRecord.Id)
	if err != nil {
		log.Printf("[ProxySettings] Error loading filters: %v, using empty filters", err)
		filterstring = ""
	}

	proxy.Filters = filterstring
	log.Printf("[ProxySettings] Filters: %s", filterstring)

	return nil
}

type ProxyBody struct {
	HTTP    string `json:"http,omitempty"`
	Browser string `json:"browser,omitempty"`
	Name    string `json:"name,omitempty"`    // Optional name for the proxy instance
	Project string `json:"project,omitempty"` // Project name to tag traffic with
}

func (backend *Backend) InitializeProxy() error {
	log.Println("[InitializeProxy] Initializing proxy index from database...")
	if ProxyMgr.proxyIndex.Load() == 0 {
		if err := ProxyMgr.initializeIndexFromDB(backend); err != nil {
			log.Printf("[StartProxy] Warning: Failed to initialize proxy index from database: %v", err)
			return err
		}
	}
	log.Println("[InitializeProxy] Proxy index initialized from database:", ProxyMgr.index.Load())
	return nil
}

// AutoStartProxy starts a headless proxy on the specified port.
// Called at startup to ensure a proxy is always available.
func (backend *Backend) AutoStartProxy(port string) (map[string]any, error) {
	return backend.startProxyLogic(&ProxyBody{
		HTTP:    "127.0.0.1:" + port,
		Browser: "none",
		Name:    "default",
	})
}

// startProxyLogic contains the core proxy start logic, shared by the HTTP handler and MCP tool.
// Returns a result map on success, or an error.
func (backend *Backend) startProxyLogic(body *ProxyBody) (map[string]any, error) {
	log.Println("[startProxyLogic] begins", body)

	if body.HTTP == "" && body.Browser != "" {
		body.HTTP = "127.0.0.1:9797"
	}

	availableHost, err := utils.CheckAndFindAvailablePort(body.HTTP)
	if err != nil {
		return nil, fmt.Errorf("port check failed: %w", err)
	}

	if body.Browser == "" && availableHost != body.HTTP {
		return map[string]any{"error": "port not available", "availableHost": availableHost}, nil
	}
	body.HTTP = availableHost

	// Initialize proxy index from database if not already initialized
	if ProxyMgr.proxyIndex.Load() == 0 {
		if err := ProxyMgr.initializeProxyIndexFromDB(backend); err != nil {
			log.Printf("[startProxyLogic] Warning: Failed to initialize proxy index from database: %v", err)
		}
	}

	// Generate unique proxy ID (this will be the primary ID, not the listen address)
	proxyID := ProxyMgr.GetNextProxyID()
	log.Printf("[startProxyLogic] Generated proxy ID: %s for address: %s", proxyID, body.HTTP)

	// Create new rawproxy wrapper
	configDir := path.Join(backend.Config.ConfigDirectory)

	// Disable file captures by passing empty string (we save to database instead)
	outputDir := "" // Empty = disabled

	newProxy, err := NewRawProxyWrapper(body.HTTP, configDir, outputDir, backend, proxyID, body.Project)
	if err != nil {
		return nil, fmt.Errorf("failed to create proxy: %w", err)
	}

	// Generate label if not provided
	label := body.Name
	browserType := body.Browser
	if browserType == "" {
		label = body.HTTP
	} else if label == "" {
		// Generate label in format: {browser} {instance_number}
		ProxyMgr.mu.RLock()
		count := 0
		for _, inst := range ProxyMgr.instances {
			if inst != nil && (inst.Browser == browserType || (browserType == "proxy" && inst.Browser == "")) {
				count++
			}
		}
		ProxyMgr.mu.RUnlock()
		count++

		if count > 1 {
			label = fmt.Sprintf("%s %d", browserType, count)
		} else {
			label = browserType
		}
	}

	// Create complete proxy instance with all fields
	proxyInstance := &ProxyInstance{
		Proxy:      newProxy,
		Browser:    body.Browser,
		BrowserCmd: nil, // Will be set later if browser is launched
		Label:      label,
		Project:    body.Project,
	}

	// Add complete instance to manager using the formatted ID as key
	ProxyMgr.AddProxyInstance(proxyID, proxyInstance)

	// Update PROXY for backward compatibility
	updateProxyVar()

	// Start the proxy
	if err := newProxy.RunProxy(); err != nil {
		return nil, fmt.Errorf("failed to start proxy: %w", err)
	}

	if body.Browser != "" {
		// Use the certificate path from the rawproxy
		certPath := newProxy.GetCertPath()

		// Generate browser profile directory: [projectid]+[proxyid]
		profileID := backend.Config.ProjectID + proxyID
		profileDir := path.Join(backend.Config.ConfigDirectory, "profiles", profileID)
		log.Printf("[startProxyLogic] Browser profile directory: %s", profileDir)

		startURL := backend.Config.InterceptedPagePath()
		go func(proxyID, browserType, listenAddr, cert, profDir, startURL string) {
			cmd, err := browser.LaunchBrowser(browserType, listenAddr, cert, profDir, startURL)
			if err != nil {
				log.Println("Error launching browser:", err)
				return
			}
			ProxyMgr.mu.Lock()
			if inst := ProxyMgr.instances[proxyID]; inst != nil {
				inst.Browser = browserType
				inst.BrowserCmd = cmd
			}
			ProxyMgr.mu.Unlock()
		}(proxyID, body.Browser, body.HTTP, certPath, profileDir, startURL)
	}

	// Create proxy record in database
	proxyRecord := lorgdb.NewRecord("_proxies")
	proxyRecord.Set("id", proxyID)
	proxyRecord.Set("project", body.Project)
	proxyRecord.Set("label", label)
	proxyRecord.Set("addr", body.HTTP)
	proxyRecord.Set("browser", body.Browser)
	proxyRecord.Set("intercept", false) // Default to false
	proxyRecord.Set("state", "running")
	proxyRecord.Set("color", "")
	proxyRecord.Set("profile", "")

	// Initialize data column (filters are now stored separately in _ui collection)
	proxyData := map[string]interface{}{}
	proxyRecord.Set("data", proxyData)

	if err := backend.DB.SaveRecord(proxyRecord); err != nil {
		return nil, fmt.Errorf("failed to save proxy record: %v", err)
	}

	log.Printf("[startProxyLogic] Created proxy record in database with ID: %s", proxyID)

	return map[string]any{
		"id":         proxyID,
		"listenAddr": body.HTTP,
		"label":      label,
		"browser":    body.Browser,
	}, nil
}

func (backend *Backend) StartProxy(e *echo.Echo) {
	e.POST("/api/proxy/start", func(c echo.Context) error {
		if err := requireAuth(c); err != nil {
			return err
		}

		var body ProxyBody
		if err := c.Bind(&body); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
		}

		result, err := backend.startProxyLogic(&body)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
		}

		return c.JSON(http.StatusOK, result)
	})
}

// updateProxyState updates the state field of a proxy record
func (backend *Backend) updateProxyState(proxyID string, state string) {
	proxyRecord, err := backend.DB.FindRecordById("_proxies", proxyID)
	if err != nil {
		log.Printf("[ProxyState][WARN] Failed to find proxy record %s: %v", proxyID, err)
		return
	}

	proxyRecord.Set("state", state)
	if err := backend.DB.SaveRecord(proxyRecord); err != nil {
		log.Printf("[ProxyState][WARN] Failed to update proxy state for %s: %v", proxyID, err)
	} else {
		log.Printf("[ProxyState] Updated proxy %s state to: %s", proxyID, state)
	}
}

func (backend *Backend) StopProxy(e *echo.Echo) {
	e.POST("/api/proxy/stop", func(c echo.Context) error {
		if err := requireAuth(c); err != nil {
			return err
		}

		type StopProxyBody struct {
			ID string `json:"id,omitempty"` // Formatted ID like "______________1"
		}

		var body StopProxyBody
		if err := c.Bind(&body); err != nil {
			// If no body provided and field is optional, stop all proxies
			log.Println("[StopProxy] No body or empty body provided, stopping all proxies")
			proxyIDs := ProxyMgr.GetAllProxies()
			for _, proxyID := range proxyIDs {
				if err := ProxyMgr.StopProxy(proxyID); err != nil {
					log.Printf("[WARN] Error stopping proxy %s: %v", proxyID, err)
				}
				backend.updateProxyState(proxyID, "")
				ProxyMgr.RemoveProxy(proxyID)
			}
		} else if body.ID != "" {
			// Stop specific proxy by ID
			proxyID := body.ID
			log.Printf("[StopProxy] Stopping specific proxy: %s", proxyID)

			// Check if proxy exists
			if proxy := ProxyMgr.GetProxy(proxyID); proxy == nil {
				log.Printf("[StopProxy][WARN] Proxy %s not found in manager", proxyID)
				return c.JSON(http.StatusNotFound, map[string]interface{}{"error": fmt.Sprintf("Proxy %s not found", proxyID)})
			}

			if err := ProxyMgr.StopProxy(proxyID); err != nil {
				log.Printf("[StopProxy][ERROR] Failed to stop proxy %s: %v", proxyID, err)
				return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
			}

			backend.updateProxyState(proxyID, "")
			log.Printf("[StopProxy] Removing proxy %s from manager", proxyID)
			ProxyMgr.RemoveProxy(proxyID)
		} else {
			// No ID field, stop all proxies
			log.Println("[StopProxy] ID field not specified, stopping all proxies")
			proxyIDs := ProxyMgr.GetAllProxies()
			for _, proxyID := range proxyIDs {
				if err := ProxyMgr.StopProxy(proxyID); err != nil {
					log.Printf("[WARN] Error stopping proxy %s: %v", proxyID, err)
				}
				backend.updateProxyState(proxyID, "")
				ProxyMgr.RemoveProxy(proxyID)
			}
		}

		// Update PROXY for backward compatibility
		updateProxyVar()

		return c.JSON(http.StatusOK, map[string]any{"message": "Proxy stopped"})
	})
}

func (backend *Backend) RestartProxy(e *echo.Echo) {
	e.POST("/api/proxy/restart", func(c echo.Context) error {
		if err := requireAuth(c); err != nil {
			return err
		}

		type RestartProxyBody struct {
			ID string `json:"id"` // Formatted ID like "______________1"
		}

		var body RestartProxyBody
		if err := c.Bind(&body); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid request body"})
		}

		if body.ID == "" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Proxy ID is required"})
		}

		proxyID := body.ID
		log.Printf("[RestartProxy] Restarting proxy: %s", proxyID)

		// Check if proxy is already running
		if ProxyMgr.GetProxy(proxyID) != nil {
			return c.JSON(http.StatusConflict, map[string]interface{}{"error": "Proxy is already running"})
		}

		// Get the proxy record from database
		proxyRecord, err := backend.DB.FindRecordById("_proxies", proxyID)
		if err != nil {
			log.Printf("[RestartProxy] Proxy record not found: %s", proxyID)
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Proxy record not found"})
		}

		// Read proxy configuration from record
		listenAddr := proxyRecord.GetString("addr")
		browserType := proxyRecord.GetString("browser")
		label := proxyRecord.GetString("label")

		log.Printf("[RestartProxy] Found proxy config - addr: %s, browser: %s, label: %s", listenAddr, browserType, label)

		// Check if port is available
		availableHost, err := utils.CheckAndFindAvailablePort(listenAddr)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
		}

		if availableHost != listenAddr {
			if browserType == "" {
				return c.JSON(http.StatusConflict, map[string]interface{}{
					"error":         "port not available",
					"availableHost": availableHost,
				})
			}
		}

		// Initialize global index from database if not already initialized
		if ProxyMgr.index.Load() == 0 {
			if err := ProxyMgr.initializeIndexFromDB(backend); err != nil {
				log.Printf("[RestartProxy] Warning: Failed to initialize global index from database: %v", err)
			}
		}

		// Create new rawproxy wrapper with existing ID
		configDir := path.Join(backend.Config.ConfigDirectory)
		outputDir := "" // Disabled

		listenAddr = availableHost

		existingProject := proxyRecord.GetString("project")
		newProxy, err := NewRawProxyWrapper(listenAddr, configDir, outputDir, backend, proxyID, existingProject)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
		}

		// Update proxy record state to running
		proxyRecord.Set("state", "running")
		proxyRecord.Set("addr", listenAddr)
		if err := backend.DB.SaveRecord(proxyRecord); err != nil {
			log.Printf("[RestartProxy][WARN] Failed to update proxy state: %v", err)
		}

		// Create proxy instance
		proxyInstance := &ProxyInstance{
			Proxy:      newProxy,
			Browser:    browserType,
			BrowserCmd: nil,
			Label:      label,
		}

		// Add to manager with the same ID
		ProxyMgr.AddProxyInstance(proxyID, proxyInstance)

		// Update PROXY for backward compatibility
		updateProxyVar()

		// Load intercept and filter settings from proxy record
		if err := backend.loadProxySettings(newProxy, proxyRecord); err != nil {
			log.Printf("[RestartProxy] Warning: Failed to load proxy settings: %v", err)
		}

		// Start the proxy
		if err := newProxy.RunProxy(); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
		}

		// Launch browser if configured
		if browserType != "" {
			certPath := newProxy.GetCertPath()

			// Generate browser profile directory: [projectid]+[proxyid]
			profileID := backend.Config.ProjectID + proxyID
			profileDir := path.Join(backend.Config.ConfigDirectory, "profiles", profileID)
			log.Printf("[RestartProxy] Browser profile directory: %s", profileDir)

			startURL := backend.Config.InterceptedPagePath()
			go func(proxyID, browserType, listenAddr, cert, profDir, startURL string) {
				cmd, err := browser.LaunchBrowser(browserType, listenAddr, cert, profDir, startURL)
				if err != nil {
					log.Println("Error launching browser:", err)
					return
				}
				ProxyMgr.mu.Lock()
				if inst := ProxyMgr.instances[proxyID]; inst != nil {
					inst.Browser = browserType
					inst.BrowserCmd = cmd
				}
				ProxyMgr.mu.Unlock()
			}(proxyID, browserType, listenAddr, certPath, profileDir, startURL)
		}

		log.Printf("[RestartProxy] Successfully restarted proxy %s", proxyID)

		return c.JSON(http.StatusOK, map[string]any{
			"id":         proxyID,
			"listenAddr": listenAddr,
			"label":      label,
			"browser":    browserType,
		})
	})
}

func (backend *Backend) ListProxies(e *echo.Echo) {
	e.GET("/api/proxy/list", func(c echo.Context) error {
		if err := requireAuth(c); err != nil {
			return err
		}

		ProxyMgr.mu.RLock()
		instances := make([]map[string]interface{}, 0, len(ProxyMgr.instances))
		for id, inst := range ProxyMgr.instances {
			if inst != nil && inst.Proxy != nil {
				var browserPid int
				if inst.BrowserCmd != nil && inst.BrowserCmd.Process != nil {
					browserPid = inst.BrowserCmd.Process.Pid
				}
				instances = append(instances, map[string]interface{}{
					"id":         id,                    // Formatted ID like "______________1"
					"listenAddr": inst.Proxy.listenAddr, // Listen address like "127.0.0.1:8080"
					"label":      inst.Label,
					"browser":    inst.Browser,
					"browserPid": browserPid,
					"project":    inst.Project,
				})
			}
		}
		ProxyMgr.mu.RUnlock()

		return c.JSON(http.StatusOK, map[string]interface{}{
			"proxies": instances,
			"count":   len(instances),
		})
	})
}
