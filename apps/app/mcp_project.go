package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/campbellcharlie/lorg/internal/lorgdb"
	"github.com/campbellcharlie/lorg/internal/types"
	"github.com/mark3labs/mcp-go/mcp"
	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// ProjectDB -- package-level singleton for live traffic recording
// ---------------------------------------------------------------------------

// InitProjectsDir is the canonical boot-time entry point for setting the
// directory the UI project switcher should scan for .db files. Wraps
// projectDB.Init so callers in cmd/ don't need to touch the singleton.
func InitProjectsDir(dbDir string) error {
	return projectDB.Init(dbDir)
}

// projectDB is the package-level singleton for live traffic recording.
// Package-level because action-dispatch handlers access it without Backend reference.
// Thread-safe: all methods on ProjectDB use internal mutex protection.
var projectDB = &ProjectDB{}

// trafficLoggingConfig controls which traffic sources are logged to the project DB.
type trafficLoggingConfig struct {
	mu      sync.RWMutex
	enabled bool
	sources map[string]bool // keys: "proxy", "repeater", "mcp", "template", "all"
}

var trafficLogging = &trafficLoggingConfig{
	enabled: true,
	sources: map[string]bool{"all": true},
}

func (c *trafficLoggingConfig) shouldLog(generatedBy string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.enabled {
		return false
	}
	if c.sources["all"] {
		return true
	}

	// Map generatedBy to source category
	source := "other"
	switch {
	case strings.HasPrefix(generatedBy, "proxy/"):
		source = "proxy"
	case strings.HasPrefix(generatedBy, "repeater/"):
		source = "repeater"
	case strings.HasPrefix(generatedBy, "ai/mcp/"):
		source = "mcp"
	case strings.Contains(generatedBy, "template"):
		source = "template"
	}

	return c.sources[source]
}

// ProjectDB maintains an open SQLite connection for real-time traffic logging.
// All exported methods are goroutine-safe.
type ProjectDB struct {
	mu    sync.Mutex
	db    *sql.DB
	name  string // current project name (e.g. "MyProject")
	dbPath string // full path to the current .db file
	dbDir  string // directory where .db files live
	ready  bool
}

// Init opens the default TemporaryProject.db in dbDir.
// If dbDir is empty, it defaults to the user's home directory.
// Safe to call multiple times; subsequent calls are no-ops if already ready.
func (p *ProjectDB) Init(dbDir string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.ready {
		return nil
	}

	if dbDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("projectDB.Init: cannot determine home directory: %w", err)
		}
		dbDir = home
	}

	p.dbDir = dbDir
	return p.openLocked("TemporaryProject")
}

// SetProject closes the current DB (if any) and opens/creates a new one
// for the given project name. If dbDir is empty the existing p.dbDir is kept;
// if that is also empty, the user's home directory is used.
func (p *ProjectDB) SetProject(name string, dbDir string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if dbDir != "" {
		p.dbDir = dbDir
	}
	if p.dbDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("projectDB.SetProject: cannot determine home directory: %w", err)
		}
		p.dbDir = home
	}

	// Close existing DB if open
	if p.db != nil {
		_ = p.db.Close()
		p.db = nil
		p.ready = false
	}

	return p.openLocked(name)
}

// openLocked opens (or creates) the SQLite DB for the given project name.
// Caller must hold p.mu.
func (p *ProjectDB) openLocked(name string) error {
	sanitized := sanitizeProjectName(name)
	if sanitized == "" {
		sanitized = "TemporaryProject"
	}

	dbFile := filepath.Join(p.dbDir, sanitized+".db")

	// Ensure directory exists
	if err := os.MkdirAll(p.dbDir, 0755); err != nil {
		return fmt.Errorf("projectDB: failed to create db directory %s: %w", p.dbDir, err)
	}

	// Check if the DB file already exists (for append vs create tracking)
	_, existErr := os.Stat(dbFile)
	isNew := os.IsNotExist(existErr)

	db, err := sql.Open("sqlite", dbFile)
	if err != nil {
		return fmt.Errorf("projectDB: failed to open database %s: %w", dbFile, err)
	}

	// Enable WAL mode for concurrent reads during writes
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return fmt.Errorf("projectDB: failed to set WAL mode: %w", err)
	}

	// Reasonable busy timeout for concurrent goroutine access
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return fmt.Errorf("projectDB: failed to set busy_timeout: %w", err)
	}

	// Initialize schema if needed (safe for append -- checks for existing tables)
	if err := initProjectSchema(db, isNew); err != nil {
		db.Close()
		return fmt.Errorf("projectDB: schema init failed: %w", err)
	}

	p.db = db
	p.name = sanitized
	p.dbPath = dbFile
	p.ready = true

	status := "opened existing"
	if isNew {
		status = "created new"
	}
	log.Printf("[ProjectDB] %s database: %s", status, dbFile)
	return nil
}

// initProjectSchema creates the burp-mcp-enhanced schema tables if they do not
// already exist. For an existing DB this is a safe no-op.
func initProjectSchema(db *sql.DB, isNew bool) error {
	if !isNew {
		// Check if the schema already exists by looking for http_traffic table
		var tableName string
		err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='http_traffic'").Scan(&tableName)
		if err == nil {
			// Table exists -- schema is already initialized
			return nil
		}
		// Table does not exist; fall through to create schema
	}

	for _, stmt := range burpMCPSchema {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("statement failed: %w\n  SQL: %s", err, stmt)
		}
	}

	// Insert schema_version record (version 4, matching burp-mcp-enhanced)
	nowMs := time.Now().UnixMilli()
	if _, err := db.Exec("INSERT OR IGNORE INTO schema_version (version, applied_at) VALUES (4, ?)", nowMs); err != nil {
		return fmt.Errorf("failed to insert schema_version: %w", err)
	}

	return nil
}

// LogTraffic writes a single traffic record to the project SQLite DB.
// It is designed to be called from a goroutine: go projectDB.LogTraffic(...)
// If no DB is open it returns nil silently.
func (p *ProjectDB) LogTraffic(userdata types.UserData, rawReq, rawResp string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.db == nil || !p.ready {
		return nil // silently skip if no DB
	}

	// Check if logging is enabled for this traffic source
	if !trafficLogging.shouldLog(userdata.GeneratedBy) {
		return nil
	}

	// Derive host -- strip protocol prefix
	host := userdata.Host
	if u, parseErr := url.Parse(host); parseErr == nil && u.Host != "" {
		host = u.Host
	} else {
		host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
	}

	method := userdata.ReqJson.Method
	path := userdata.ReqJson.Path
	query := userdata.ReqJson.Query
	ext := userdata.ReqJson.Ext
	status := userdata.RespJson.Status
	respLength := userdata.RespJson.Length
	mime := userdata.RespJson.Mime
	title := userdata.RespJson.Title

	// Derive protocol and port
	protocol := "http"
	if userdata.IsHTTPS {
		protocol = "https"
	}
	port := 80
	if userdata.Port != "" {
		fmt.Sscanf(userdata.Port, "%d", &port)
	} else if userdata.IsHTTPS {
		port = 443
	}

	// Build full URL
	fullURL := fmt.Sprintf("%s://%s", protocol, host)
	if (protocol == "https" && port != 443) || (protocol == "http" && port != 80) {
		fullURL = fmt.Sprintf("%s:%d", fullURL, port)
	}
	if path != "" {
		fullURL += path
	}
	if query != "" {
		fullURL += "?" + query
	}

	// Map generated_by to tool name
	tool := mapGeneratedByToTool(userdata.GeneratedBy)

	// Count parameters
	paramCount := 0
	if query != "" {
		if vals, parseErr := url.ParseQuery(query); parseErr == nil {
			paramCount = len(vals)
		}
	}

	// Content-Type from response headers
	contentType := ""
	for k, v := range userdata.RespJson.Headers {
		if strings.EqualFold(k, "content-type") {
			contentType = v
			break
		}
	}

	// Split raw request/response into headers + body
	reqHeaders, reqBody := splitHTTPRaw(rawReq)
	respHeaders, respBody := splitHTTPRaw(rawResp)

	// Generate request_hash: first 16 chars of SHA-256 of raw request
	requestHash := ""
	if rawReq != "" {
		h := sha256.Sum256([]byte(rawReq))
		requestHash = hex.EncodeToString(h[:])[:16]
	}

	timestamp := time.Now().UTC().Format(time.RFC3339)

	// INSERT OR IGNORE prevents duplicates via the request_hash unique constraint
	result, err := p.db.Exec(`INSERT OR IGNORE INTO http_traffic
		(timestamp, tool, method, host, path, query, param_count, status_code,
		 response_length, protocol, port, url, mime_type, extension, page_title,
		 content_type, request_hash, session_tag)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		timestamp,
		tool,
		method,
		host,
		path,
		query,
		paramCount,
		status,
		respLength,
		protocol,
		port,
		fullURL,
		mime,
		ext,
		title,
		contentType,
		requestHash,
		"", // session_tag
	)
	if err != nil {
		return fmt.Errorf("projectDB.LogTraffic: traffic insert failed: %w", err)
	}

	requestID, err := result.LastInsertId()
	if err != nil || requestID == 0 {
		// requestID == 0 means INSERT OR IGNORE skipped (duplicate hash)
		return nil
	}

	// Insert message record
	_, _ = p.db.Exec(`INSERT OR IGNORE INTO http_messages
		(request_id, request_headers, request_body, response_headers, response_body)
		VALUES (?, ?, ?, ?, ?)`,
		requestID, reqHeaders, []byte(reqBody), respHeaders, []byte(respBody),
	)

	// Insert FTS entry
	_, _ = p.db.Exec(`INSERT INTO traffic_fts (rowid, url, request_headers, request_body, response_headers, response_body)
		VALUES (?, ?, ?, ?, ?, ?)`,
		requestID, fullURL, reqHeaders, reqBody, respHeaders, respBody,
	)

	return nil
}

// Close closes the current database connection.
func (p *ProjectDB) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.db != nil {
		_ = p.db.Close()
		p.db = nil
	}
	p.ready = false
}

// Info returns a snapshot of the current project state.
func (p *ProjectDB) Info() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()

	info := map[string]any{
		"projectName":  p.name,
		"dbPath":       p.dbPath,
		"dbDir":        p.dbDir,
		"isActive":     p.ready,
		"trafficCount": 0,
	}

	if p.db != nil && p.ready {
		var count int
		if err := p.db.QueryRow("SELECT count(*) FROM http_traffic").Scan(&count); err == nil {
			info["trafficCount"] = count
		}
	}

	return info
}

// ---------------------------------------------------------------------------
// Input schemas
// ---------------------------------------------------------------------------

type ProjectSetupArgs struct {
	Name  string `json:"name" jsonschema:"required" jsonschema_description:"Project name. Creates {name}.db or appends to existing."`
	DbDir string `json:"dbDir,omitempty" jsonschema_description:"Directory for SQLite DB files. Defaults to ~/"`
}

type ProjectExportArgs struct {
	OutputPath  string `json:"outputPath" jsonschema:"required" jsonschema_description:"Output path for the SQLite database file"`
	ProjectName string `json:"projectName" jsonschema:"required" jsonschema_description:"Project name (used in DB metadata)"`
	HostFilter  string `json:"hostFilter,omitempty" jsonschema_description:"Only export traffic for this host"`
}

type ProjectInfoArgs struct{}

type ProjectSetNameArgs struct {
	Name string `json:"name" jsonschema:"required" jsonschema_description:"Project name"`
}

// ---------------------------------------------------------------------------
// projectSetup handler
// ---------------------------------------------------------------------------

func (backend *Backend) projectSetupHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ProjectSetupArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	if strings.TrimSpace(args.Name) == "" {
		return mcp.NewToolResultError("project name cannot be empty"), nil
	}

	sanitized := sanitizeProjectName(args.Name)
	dbDir := args.DbDir

	// Check if the DB file will be new or existing
	checkDir := dbDir
	if checkDir == "" {
		checkDir = projectDB.dbDir
		if checkDir == "" {
			checkDir, _ = os.UserHomeDir()
		}
	}
	dbFile := filepath.Join(checkDir, sanitized+".db")
	_, existErr := os.Stat(dbFile)
	isNew := os.IsNotExist(existErr)

	if err := projectDB.SetProject(args.Name, dbDir); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to setup project: %v", err)), nil
	}

	// Also store the project name in PocketBase _settings for backward compat
	backend.storeProjectNameInPB(sanitized)

	return mcpJSONResult(map[string]any{
		"success":     true,
		"projectName": sanitized,
		"dbPath":      projectDB.dbPath,
		"isNew":       isNew,
	})
}

// ---------------------------------------------------------------------------
// projectInfo handler
// ---------------------------------------------------------------------------

func (backend *Backend) projectInfoHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Lazy init: ensure projectDB is initialized
	_ = projectDB.Init("")

	info := projectDB.Info()

	// Augment with host count from lorgdb for backward compat
	allData, _ := backend.DB.FindRecords("_data", "1=1")

	hostSet := map[string]struct{}{}
	for _, rec := range allData {
		h := rec.GetString("host")
		if h != "" {
			hostSet[h] = struct{}{}
		}
	}

	info["hostCount"] = len(hostSet)
	info["pocketbaseTrafficCount"] = len(allData)

	return mcpJSONResult(info)
}

// ---------------------------------------------------------------------------
// projectSetName handler -- backward compat alias for projectSetup
// ---------------------------------------------------------------------------

func (backend *Backend) projectSetNameHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ProjectSetNameArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	if strings.TrimSpace(args.Name) == "" {
		return mcp.NewToolResultError("project name cannot be empty"), nil
	}

	// Lazy init if needed
	_ = projectDB.Init("")

	if err := projectDB.SetProject(args.Name, projectDB.dbDir); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to set project name: %v", err)), nil
	}

	// Also store in PocketBase for backward compat
	backend.storeProjectNameInPB(sanitizeProjectName(args.Name))

	return mcpJSONResult(map[string]any{
		"success":     true,
		"projectName": sanitizeProjectName(args.Name),
		"dbPath":      projectDB.dbPath,
	})
}

// storeProjectNameInPB persists the project name in PocketBase _settings
// for backward compatibility with the UI and other tools.
func (backend *Backend) storeProjectNameInPB(name string) {
	record, err := backend.DB.FindFirstRecord("_settings", "option = ?", "project_name")
	if err != nil || record == nil {
		record = lorgdb.NewRecord("_settings")
		record.Set("option", "project_name")
	}
	record.Set("value", name)
	_ = backend.DB.SaveRecord(record)
}

// ---------------------------------------------------------------------------
// setTrafficLogging handler
// ---------------------------------------------------------------------------

type SetTrafficLoggingArgs struct {
	Enabled bool     `json:"enabled" jsonschema:"required" jsonschema_description:"Enable or disable traffic logging to project DB"`
	Sources []string `json:"sources,omitempty" jsonschema_description:"Which sources to log: proxy, repeater, mcp, template, all (default: all)"`
}

func (backend *Backend) setTrafficLoggingHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args SetTrafficLoggingArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	trafficLogging.mu.Lock()
	trafficLogging.enabled = args.Enabled
	trafficLogging.sources = make(map[string]bool)
	if len(args.Sources) == 0 {
		trafficLogging.sources["all"] = true
	} else {
		for _, s := range args.Sources {
			trafficLogging.sources[strings.ToLower(s)] = true
		}
	}
	trafficLogging.mu.Unlock()

	return mcpJSONResult(map[string]any{
		"success": true,
		"enabled": args.Enabled,
		"sources": args.Sources,
	})
}

// ---------------------------------------------------------------------------
// projectExport handler -- full export from PocketBase for reimporting old data
// ---------------------------------------------------------------------------

// parseJSONField handles PocketBase JSON fields that may be stored as text strings,
// types.JsonRaw, []byte, or already-parsed maps.
func parseJSONField(v any) map[string]any {
	if v == nil {
		return nil
	}
	// If it's already a map, return it
	if m, ok := v.(map[string]any); ok {
		return m
	}
	// If it's a string, parse it as JSON
	if s, ok := v.(string); ok && s != "" {
		var m map[string]any
		if json.Unmarshal([]byte(s), &m) == nil {
			return m
		}
	}
	// If it's []byte or json.RawMessage
	if b, ok := v.([]byte); ok && len(b) > 0 {
		var m map[string]any
		if json.Unmarshal(b, &m) == nil {
			return m
		}
	}
	// If it implements fmt.Stringer (e.g. types.JsonRaw)
	if s, ok := v.(fmt.Stringer); ok {
		str := s.String()
		if str != "" {
			var m map[string]any
			if json.Unmarshal([]byte(str), &m) == nil {
				return m
			}
		}
	}
	// Last resort: marshal to JSON and re-parse (handles custom types)
	if b, err := json.Marshal(v); err == nil && len(b) > 2 {
		var m map[string]any
		if json.Unmarshal(b, &m) == nil {
			return m
		}
	}
	return nil
}

// burpMCPSchema is the exact schema used by burp-mcp-enhanced.
// It is executed as a sequence of statements when creating the export database.
var burpMCPSchema = []string{
	`CREATE TABLE schema_version (
    version INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL
)`,
	`CREATE TABLE http_traffic (
    request_id    INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp     TEXT    NOT NULL,
    tool          TEXT    NOT NULL,
    method        TEXT    NOT NULL,
    host          TEXT    NOT NULL,
    path          TEXT,
    query         TEXT,
    param_count   INTEGER,
    status_code   INTEGER,
    response_length INTEGER,
    request_time  TEXT,
    comment       TEXT,
    protocol      TEXT    NOT NULL,
    port          INTEGER NOT NULL,
    url           TEXT    NOT NULL,
    ip_address    TEXT,
    param_names   TEXT,
    mime_type     TEXT,
    extension     TEXT,
    page_title    TEXT,
    response_time TEXT,
    connection_id TEXT,
    content_type  TEXT,
    request_hash  TEXT UNIQUE,
    session_tag   TEXT,
    notes         TEXT
)`,
	`CREATE TABLE http_messages (
    request_id       INTEGER PRIMARY KEY,
    request_headers  TEXT,
    request_body     BLOB,
    response_headers TEXT,
    response_body    BLOB,
    FOREIGN KEY (request_id) REFERENCES http_traffic(request_id)
)`,
	`CREATE VIRTUAL TABLE traffic_fts USING fts5(
    url,
    request_headers,
    request_body,
    response_headers,
    response_body,
    content=''
)`,
	`CREATE TABLE sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT UNIQUE NOT NULL,
    created_at INTEGER NOT NULL,
    cookies TEXT,
    headers TEXT,
    notes TEXT
)`,
	`CREATE TABLE templates (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT UNIQUE NOT NULL,
    created_at INTEGER NOT NULL,
    template_json TEXT NOT NULL
)`,
	`CREATE TABLE traffic_tags (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    traffic_id INTEGER NOT NULL,
    tag TEXT NOT NULL,
    note TEXT,
    created_at INTEGER DEFAULT (strftime('%s', 'now') * 1000),
    FOREIGN KEY (traffic_id) REFERENCES http_traffic(request_id) ON DELETE CASCADE
)`,
	`CREATE TABLE raw_socket_traffic (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp TEXT NOT NULL,
    tool TEXT NOT NULL,
    target_host TEXT NOT NULL,
    target_port INTEGER NOT NULL,
    protocol TEXT NOT NULL,
    alpn_negotiated TEXT,
    request_bytes BLOB,
    response_bytes BLOB,
    response_preview TEXT,
    bytes_sent INTEGER,
    bytes_received INTEGER,
    elapsed_ms INTEGER,
    segment_count INTEGER,
    connection_count INTEGER,
    notes TEXT
)`,
	`CREATE TABLE collaborator_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp TEXT NOT NULL,
    event_type TEXT NOT NULL,
    client_id TEXT,
    payload_url TEXT,
    custom_data TEXT,
    interaction_type TEXT,
    interaction_id TEXT,
    dns_query TEXT,
    dns_query_type TEXT,
    http_protocol TEXT,
    smtp_protocol TEXT,
    server_address TEXT,
    notes TEXT
)`,
	// Indexes
	`CREATE INDEX idx_timestamp ON http_traffic(timestamp)`,
	`CREATE INDEX idx_host ON http_traffic(host)`,
	`CREATE INDEX idx_status_code ON http_traffic(status_code)`,
	`CREATE INDEX idx_tool ON http_traffic(tool)`,
	`CREATE INDEX idx_method ON http_traffic(method)`,
	`CREATE INDEX idx_host_timestamp ON http_traffic(host, timestamp DESC)`,
	`CREATE INDEX idx_session ON http_traffic(session_tag, timestamp DESC)`,
	`CREATE INDEX idx_method_url ON http_traffic(method, url)`,
	`CREATE INDEX idx_traffic_tags_tag ON traffic_tags(tag)`,
	`CREATE INDEX idx_traffic_tags_traffic_id ON traffic_tags(traffic_id)`,
	`CREATE INDEX idx_raw_socket_timestamp ON raw_socket_traffic(timestamp)`,
	`CREATE INDEX idx_raw_socket_host ON raw_socket_traffic(target_host)`,
	`CREATE INDEX idx_raw_socket_tool ON raw_socket_traffic(tool)`,
	`CREATE INDEX idx_collab_timestamp ON collaborator_events(timestamp)`,
	`CREATE INDEX idx_collab_client_id ON collaborator_events(client_id)`,
	`CREATE INDEX idx_collab_event_type ON collaborator_events(event_type)`,
}

func (backend *Backend) projectExportHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ProjectExportArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	if args.OutputPath == "" {
		return mcp.NewToolResultError("outputPath is required"), nil
	}
	if args.ProjectName == "" {
		return mcp.NewToolResultError("projectName is required"), nil
	}

	// Ensure parent directory exists
	dir := filepath.Dir(args.OutputPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to create output directory: %v", err)), nil
	}

	// Remove existing file if present so we start fresh
	_ = os.Remove(args.OutputPath)

	// Open new SQLite database
	exportDB, err := sql.Open("sqlite", args.OutputPath)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to create export database: %v", err)), nil
	}
	defer exportDB.Close()

	// Enable WAL mode for better write performance
	if _, err := exportDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to set WAL mode: %v", err)), nil
	}

	// Create schema
	for _, stmt := range burpMCPSchema {
		if _, err := exportDB.Exec(stmt); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to create schema: %v\nStatement: %s", err, stmt)), nil
		}
	}

	// Insert schema_version record (version 4, matching burp-mcp-enhanced)
	nowMs := time.Now().UnixMilli()
	if _, err := exportDB.Exec("INSERT INTO schema_version (version, applied_at) VALUES (4, ?)", nowMs); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to insert schema_version: %v", err)), nil
	}

	// -----------------------------------------------------------------
	// Export traffic
	// -----------------------------------------------------------------
	var dataRecords []*lorgdb.Record
	if args.HostFilter != "" {
		dataRecords, err = backend.DB.FindRecordsSorted("_data", "host LIKE ?", "\"index\" DESC", 0, 0, "%"+args.HostFilter+"%")
	} else {
		dataRecords, err = backend.DB.FindRecords("_data", "1=1")
	}
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to fetch traffic data: %v", err)), nil
	}

	// Prepare insert statements
	trafficStmt, err := exportDB.Prepare(`INSERT INTO http_traffic
		(timestamp, tool, method, host, path, query, param_count, status_code,
		 response_length, protocol, port, url, mime_type, extension, page_title,
		 content_type, request_hash, session_tag)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to prepare traffic insert: %v", err)), nil
	}
	defer trafficStmt.Close()

	messageStmt, err := exportDB.Prepare(`INSERT INTO http_messages
		(request_id, request_headers, request_body, response_headers, response_body)
		VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to prepare message insert: %v", err)), nil
	}
	defer messageStmt.Close()

	ftsStmt, err := exportDB.Prepare(`INSERT INTO traffic_fts (rowid, url, request_headers, request_body, response_headers, response_body)
		VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to prepare FTS insert: %v", err)), nil
	}
	defer ftsStmt.Close()

	exportedTraffic := 0

	// Use a transaction for bulk insert performance
	tx, err := exportDB.Begin()
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to begin transaction: %v", err)), nil
	}
	txTraffic := tx.Stmt(trafficStmt)
	txMessage := tx.Stmt(messageStmt)
	txFTS := tx.Stmt(ftsStmt)

	for _, rec := range dataRecords {
		id := rec.GetString("id")
		host := rec.GetString("host")
		portStr := rec.GetString("port")
		isHTTPS := rec.GetBool("is_https")
		generatedBy := rec.GetString("generated_by")
		created := rec.GetString("created") // PocketBase timestamp string

		// Parse req_json and resp_json -- PocketBase may return types.JsonRaw, string, or map
		reqJSONRaw := rec.Get("req_json")
		respJSONRaw := rec.Get("resp_json")
		reqJSON := parseJSONField(reqJSONRaw)
		respJSON := parseJSONField(respJSONRaw)

		// Debug: if still nil, try GetString and parse
		if reqJSON == nil {
			if s := rec.GetString("req_json"); s != "" {
				json.Unmarshal([]byte(s), &reqJSON)
			}
		}
		if respJSON == nil {
			if s := rec.GetString("resp_json"); s != "" {
				json.Unmarshal([]byte(s), &respJSON)
			}
		}

		method := mapStr(reqJSON, "method")
		path := mapStr(reqJSON, "path")
		query := mapStr(reqJSON, "query")
		ext := mapStr(reqJSON, "ext")

		status := int(mapFloat(respJSON, "status"))
		respLength := int(mapFloat(respJSON, "length"))
		mime := mapStr(respJSON, "mime")
		title := mapStr(respJSON, "title")

		// Derive protocol
		protocol := "http"
		if isHTTPS {
			protocol = "https"
		}

		// Derive port
		port := 80
		if portStr != "" {
			fmt.Sscanf(portStr, "%d", &port)
		} else if isHTTPS {
			port = 443
		}

		// Strip protocol prefix from host for the export
		// lorg stores host as "https://example.com" or "http://example.com"
		exportHost := host
		if u, parseErr := url.Parse(host); parseErr == nil && u.Host != "" {
			exportHost = u.Host
		}

		// Build full URL
		fullURL := fmt.Sprintf("%s://%s", protocol, exportHost)
		if (protocol == "https" && port != 443) || (protocol == "http" && port != 80) {
			fullURL = fmt.Sprintf("%s:%d", fullURL, port)
		}
		if path != "" {
			fullURL += path
		}
		if query != "" {
			fullURL += "?" + query
		}

		// Map generated_by to tool name
		tool := mapGeneratedByToTool(generatedBy)

		// Count parameters
		paramCount := 0
		if query != "" {
			if vals, parseErr := url.ParseQuery(query); parseErr == nil {
				paramCount = len(vals)
			}
		}

		// Content-Type from resp_json headers
		contentType := ""
		if respHeaders := asMap(respJSON["headers"]); respHeaders != nil {
			// Headers may be stored with various casings
			for k, v := range respHeaders {
				if strings.EqualFold(k, "content-type") {
					if s, ok := v.(string); ok {
						contentType = s
					}
					break
				}
			}
		}

		// Fetch raw request and response
		reqRaw := ""
		respRaw := ""
		if reqRec, _ := backend.DB.FindRecordById("_req", id); reqRec != nil {
			reqRaw = reqRec.GetString("raw")
		}
		if respRec, _ := backend.DB.FindRecordById("_resp", id); respRec != nil {
			respRaw = respRec.GetString("raw")
		}

		// Split raw into headers + body
		reqHeaders, reqBody := splitHTTPRaw(reqRaw)
		respHeaders, respBody := splitHTTPRaw(respRaw)

		// Generate request_hash: first 16 chars of SHA-256 of raw request
		requestHash := ""
		if reqRaw != "" {
			h := sha256.Sum256([]byte(reqRaw))
			requestHash = hex.EncodeToString(h[:])[:16]
		}

		// Use PocketBase created timestamp, fallback to now
		timestamp := created
		if timestamp == "" {
			timestamp = time.Now().UTC().Format(time.RFC3339)
		}

		// Insert traffic record
		result, err := txTraffic.Exec(
			timestamp,    // timestamp
			tool,         // tool
			method,       // method
			exportHost,   // host
			path,         // path
			query,        // query
			paramCount,   // param_count
			status,       // status_code
			respLength,   // response_length
			protocol,     // protocol
			port,         // port
			fullURL,      // url
			mime,         // mime_type
			ext,          // extension
			title,        // page_title
			contentType,  // content_type
			requestHash,  // request_hash
			"",           // session_tag
		)
		if err != nil {
			// Skip duplicates (unique constraint on request_hash)
			continue
		}

		requestID, err := result.LastInsertId()
		if err != nil {
			continue
		}

		// Insert message record
		_, _ = txMessage.Exec(requestID, reqHeaders, []byte(reqBody), respHeaders, []byte(respBody))

		// Insert FTS entry
		_, _ = txFTS.Exec(requestID, fullURL, reqHeaders, reqBody, respHeaders, respBody)

		exportedTraffic++
	}

	if err := tx.Commit(); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to commit traffic transaction: %v", err)), nil
	}

	// -----------------------------------------------------------------
	// Export sessions from _sessions collection
	// -----------------------------------------------------------------
	exportedSessions := 0
	sessionRecords, sessErr := backend.DB.FindRecords("_sessions", "1=1")
	if sessErr == nil && len(sessionRecords) > 0 {
		sessTx, txErr := exportDB.Begin()
		if txErr == nil {
			sessStmt, stmtErr := sessTx.Prepare(`INSERT INTO sessions (name, created_at, cookies, headers, notes) VALUES (?, ?, ?, ?, ?)`)
			if stmtErr == nil {
				for _, sr := range sessionRecords {
					name := sr.GetString("name")
					createdAt := time.Now().UnixMilli()

					cookiesRaw, _ := json.Marshal(sr.Get("cookies"))
					headersRaw, _ := json.Marshal(sr.Get("headers"))

					_, insertErr := sessStmt.Exec(name, createdAt, string(cookiesRaw), string(headersRaw), "")
					if insertErr == nil {
						exportedSessions++
					}
				}
				sessStmt.Close()
			}
			_ = sessTx.Commit()
		}
	}

	// -----------------------------------------------------------------
	// Export templates from _mcp_templates collection
	// -----------------------------------------------------------------
	exportedTemplates := 0
	tmplRecords, tmplErr := backend.DB.FindRecords("_mcp_templates", "1=1")
	if tmplErr == nil && len(tmplRecords) > 0 {
		tmplTx, txErr := exportDB.Begin()
		if txErr == nil {
			tmplStmt, stmtErr := tmplTx.Prepare(`INSERT INTO templates (name, created_at, template_json) VALUES (?, ?, ?)`)
			if stmtErr == nil {
				for _, tr := range tmplRecords {
					name := tr.GetString("name")
					createdAt := time.Now().UnixMilli()

					// Build a JSON representation of the template
					tmplData := map[string]any{
						"name":            name,
						"tls":             tr.GetBool("tls"),
						"host":            tr.GetString("host"),
						"port":            tr.GetFloat("port"),
						"http_version":    tr.GetFloat("http_version"),
						"request_template": tr.GetString("request_template"),
						"variables":       tr.Get("variables"),
						"description":     tr.GetString("description"),
						"inject_session":  tr.GetBool("inject_session"),
						"json_escape_vars": tr.GetBool("json_escape_vars"),
						"extract_regex":   tr.GetString("extract_regex"),
						"extract_group":   tr.GetFloat("extract_group"),
					}
					tmplJSON, _ := json.Marshal(tmplData)

					_, insertErr := tmplStmt.Exec(name, createdAt, string(tmplJSON))
					if insertErr == nil {
						exportedTemplates++
					}
				}
				tmplStmt.Close()
			}
			_ = tmplTx.Commit()
		}
	}

	return mcpJSONResult(map[string]any{
		"success":           true,
		"outputPath":        args.OutputPath,
		"exportedTraffic":   exportedTraffic,
		"exportedSessions":  exportedSessions,
		"exportedTemplates": exportedTemplates,
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// sanitizeProjectName replaces non-alphanumeric characters (except - and _)
// with underscores and caps the length at 100 characters.
var projectNameRe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func sanitizeProjectName(name string) string {
	s := strings.TrimSpace(name)
	s = projectNameRe.ReplaceAllString(s, "_")
	if len(s) > 100 {
		s = s[:100]
	}
	return s
}

// mapGeneratedByToTool converts lorg's generated_by field to a burp-style
// tool name for the export.
func mapGeneratedByToTool(generatedBy string) string {
	switch {
	case strings.HasPrefix(generatedBy, "proxy/"):
		return "Proxy"
	case strings.HasPrefix(generatedBy, "repeater/"):
		return "Repeater"
	case strings.HasPrefix(generatedBy, "ai/mcp/"):
		return "MCP"
	case strings.Contains(generatedBy, "template"):
		return "Template"
	case generatedBy == "":
		return "Proxy"
	default:
		return generatedBy
	}
}

// splitHTTPRaw splits a raw HTTP message into headers and body at the
// standard \r\n\r\n boundary (falling back to \n\n if CRLF form is absent).
func splitHTTPRaw(raw string) (headers string, body string) {
	if raw == "" {
		return "", ""
	}
	if idx := strings.Index(raw, "\r\n\r\n"); idx != -1 {
		return raw[:idx], raw[idx+4:]
	}
	if idx := strings.Index(raw, "\n\n"); idx != -1 {
		return raw[:idx], raw[idx+2:]
	}
	// No body separator found; treat entire content as headers
	return raw, ""
}
