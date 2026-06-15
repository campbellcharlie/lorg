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

// SetActiveProject points the Active write-target project DB at name, creating
// the DB if needed and keeping the configured projects directory. Used at
// startup to align the write target with an external read source (e.g. a Serval
// traffic.db under .../projects/<name>/) so reads and writes agree without a
// later setActive call or a restart.
func SetActiveProject(name string) error {
	return projectDB.SetProject(name, "")
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

	// Map generatedBy to source category. AI/MCP traffic is routed through
	// the repeater path so its generated_by carries BOTH prefixes (e.g.
	// "repeater/ai/mcp/http"). The ai/mcp substring check has to run
	// BEFORE the repeater/ prefix check or MCP traffic would be classified
	// as plain "repeater" — same root cause as the mapGeneratedByToTool
	// fix in commit 0a54f0e.
	source := "other"
	switch {
	case strings.Contains(generatedBy, "ai/mcp"):
		source = "mcp"
	case strings.HasPrefix(generatedBy, "proxy/"):
		source = "proxy"
	case strings.Contains(generatedBy, "template"):
		source = "template"
	case strings.HasPrefix(generatedBy, "repeater/"):
		source = "repeater"
	}

	return c.sources[source]
}

// ---------------------------------------------------------------------------
// Async traffic logging queue (ADR-003 A4)
// ---------------------------------------------------------------------------
//
// Capture used to spawn `go projectDB.LogTraffic(...)` per request — unbounded
// goroutines under load. Instead, enqueueTraffic hands the row to a bounded
// worker pool draining a buffered channel. Normal load: the buffered send
// returns immediately (capture stays decoupled from the DB write). Sustained
// overload: the send blocks, applying backpressure instead of dropping rows or
// spawning unbounded goroutines. The job carries its *ProjectDB so the pool is
// not tied to the package singleton (and is testable with a local instance).

type trafficJob struct {
	p       *ProjectDB
	ud      types.UserData
	rawReq  string
	rawResp string
}

const (
	trafficQueueBuffer  = 2048
	trafficQueueWorkers = 8
)

var (
	trafficQueueOnce sync.Once
	trafficQueueCh   chan trafficJob
)

func startTrafficQueue() {
	trafficQueueCh = make(chan trafficJob, trafficQueueBuffer)
	for i := 0; i < trafficQueueWorkers; i++ {
		go func() {
			for job := range trafficQueueCh {
				_ = job.p.LogTraffic(job.ud, job.rawReq, job.rawResp)
			}
		}()
	}
}

// enqueueTraffic queues a captured row for async logging. Replaces the old
// per-request `go LogTraffic`. Blocking send = backpressure when the pool is
// saturated; no dropped rows, bounded goroutines.
func (p *ProjectDB) enqueueTraffic(ud types.UserData, rawReq, rawResp string) {
	trafficQueueOnce.Do(startTrafficQueue)
	trafficQueueCh <- trafficJob{p: p, ud: ud, rawReq: rawReq, rawResp: rawResp}
}

// trafficQueueDepth reports the current backlog (for lorgStatus / load
// monitoring). Returns 0 before the queue is started.
func trafficQueueDepth() (depth, capacity int) {
	if trafficQueueCh == nil {
		return 0, 0
	}
	return len(trafficQueueCh), cap(trafficQueueCh)
}

// ProjectDB maintains an open SQLite connection for real-time traffic logging.
// All exported methods are goroutine-safe.
//
// Two pointers, by design:
//
//   - Active (db / name / dbPath): the write target. Captured proxy traffic,
//     repeater sends, intercept edits, MCP write tools — all writes go here.
//     Only changes when SetProject is called (via /api/project/setActive).
//   - Viewed (viewedDB / viewedName / viewedPath): the UI's read target.
//     Defaults to Active. SetViewed opens a separate read-only handle so the
//     user can browse another project without affecting writes. ViewedDB()
//     falls back to the Active handle when no viewer is set.
//
// MCP read tools take an optional `project` arg and resolve through the
// readDBCache below, independent of Viewed — so the AI can scope reads
// per-call without touching the user's UI selection.
type ProjectDB struct {
	// RWMutex (ADR-003 A1): LogTraffic takes the read lock for the whole insert
	// so concurrent captures across different project handles run in parallel
	// instead of serializing on one mutex; lifecycle ops (SetProject, Close,
	// handle eviction, delete) take the write lock and so wait for in-flight
	// writes — a handle can't be closed mid-insert.
	mu     sync.RWMutex
	db     *sql.DB
	name   string // current project name (e.g. "MyProject")
	dbPath string // full path to the current .db file
	dbDir  string // directory where .db files live
	ready  bool

	// Read-only viewer for the UI. Nil = "viewing active".
	viewedDB   *sql.DB
	viewedName string
	viewedPath string

	// writeHandles caches open read/write handles for projects OTHER than the
	// Active one, so a per-call addressed send (ADR-002) can log into another
	// project's DB without changing the Active write target. Keyed by sanitized
	// project name. Opened lazily by writeHandleForLocked, closed in Close().
	// Bounded to maxWriteHandles (ADR-003 A2) with FIFO eviction tracked by
	// writeHandleOrder so the registry can't grow without limit.
	writeHandles     map[string]*sql.DB
	writeHandleOrder []string
}

// maxWriteHandles caps the number of open addressed (non-Active) write handles.
// A pentest agent juggles a handful of projects; this is a backstop against
// unbounded growth, not a hot-path tuning knob. Eviction is FIFO (oldest-opened).
const maxWriteHandles = 16

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

	// One writer connection per handle (ADR-003 A1): SQLite allows a single
	// writer per DB, so capping the pool makes concurrent LogTraffic calls to the
	// SAME project queue on the connection instead of racing into SQLITE_BUSY.
	// Different projects are different handles/pools, so they still write in
	// parallel. Readers use separate WAL connections (viewedDB, readDBCache).
	db.SetMaxOpenConns(1)

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
// already exist, then runs any version-up migrations for existing DBs.
func initProjectSchema(db *sql.DB, isNew bool) error {
	if !isNew {
		// Check if the schema already exists by looking for http_traffic table
		var tableName string
		err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='http_traffic'").Scan(&tableName)
		if err == nil {
			// Table exists -- run migrations and return
			return migrateProjectSchema(db)
		}
		// Table does not exist; fall through to create schema
	}

	for _, stmt := range burpMCPSchema {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("statement failed: %w\n  SQL: %s", err, stmt)
		}
	}

	// New DBs are at the latest schema version (currently 5).
	nowMs := time.Now().UnixMilli()
	if _, err := db.Exec("INSERT OR IGNORE INTO schema_version (version, applied_at) VALUES (?, ?)", currentSchemaVersion, nowMs); err != nil {
		return fmt.Errorf("failed to insert schema_version: %w", err)
	}

	return nil
}

// currentSchemaVersion is the latest schema version this build understands.
//
//	v4: original burp-mcp-enhanced schema with `request_hash TEXT UNIQUE`
//	v5: drops the UNIQUE so identical replayed requests aren't silently
//	    deduped (a fuzz/repeater workflow needs every iteration recorded).
const currentSchemaVersion = 6

// migrateProjectSchema brings an existing project DB up to currentSchemaVersion.
// Each step is idempotent so running twice is a no-op.
func migrateProjectSchema(db *sql.DB) error {
	var version int
	if err := db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&version); err != nil {
		// Older DBs may not have a schema_version table at all
		version = 0
	}

	if version < 5 {
		if err := migrateV5DropRequestHashUnique(db); err != nil {
			return fmt.Errorf("v5 migration: %w", err)
		}
		if _, err := db.Exec("INSERT OR IGNORE INTO schema_version (version, applied_at) VALUES (5, ?)", time.Now().UnixMilli()); err != nil {
			return fmt.Errorf("v5 version stamp: %w", err)
		}
		log.Printf("[ProjectDB] migrated schema → v5 (dropped request_hash UNIQUE)")
	}

	if version < 6 {
		// ADR-004 E1: make http_traffic a superset of _data so projectDB can
		// serve the readers currently tied to lorgdb's global table. Adds the
		// _data-only columns the union read layer needs.
		alters := []string{
			"ALTER TABLE http_traffic ADD COLUMN fingerprint TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE http_traffic ADD COLUMN generated_by TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE http_traffic ADD COLUMN global_seq INTEGER NOT NULL DEFAULT 0",
		}
		for _, stmt := range alters {
			if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("v6 migration: %w", err)
			}
		}
		for _, stmt := range []string{
			"CREATE INDEX IF NOT EXISTS idx_ht_fingerprint ON http_traffic(fingerprint)",
			"CREATE INDEX IF NOT EXISTS idx_ht_global_seq ON http_traffic(global_seq)",
		} {
			if _, err := db.Exec(stmt); err != nil {
				return fmt.Errorf("v6 index: %w", err)
			}
		}
		if _, err := db.Exec("INSERT OR IGNORE INTO schema_version (version, applied_at) VALUES (6, ?)", time.Now().UnixMilli()); err != nil {
			return fmt.Errorf("v6 version stamp: %w", err)
		}
		log.Printf("[ProjectDB] migrated schema → v6 (http_traffic superset: fingerprint, generated_by, global_seq)")
	}

	return nil
}

// migrateV5DropRequestHashUnique rebuilds http_traffic without the UNIQUE
// constraint on request_hash. SQLite can't ALTER away a UNIQUE constraint,
// so the dance is: rename old → create new without UNIQUE → copy → drop old.
func migrateV5DropRequestHashUnique(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmts := []string{
		`ALTER TABLE http_traffic RENAME TO http_traffic_old_v4`,
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
			request_hash  TEXT,
			session_tag   TEXT,
			notes         TEXT
		)`,
		`INSERT INTO http_traffic SELECT * FROM http_traffic_old_v4`,
		`DROP TABLE http_traffic_old_v4`,
		`CREATE INDEX IF NOT EXISTS idx_timestamp ON http_traffic(timestamp)`,
		`CREATE INDEX IF NOT EXISTS idx_host ON http_traffic(host)`,
		`CREATE INDEX IF NOT EXISTS idx_status_code ON http_traffic(status_code)`,
		`CREATE INDEX IF NOT EXISTS idx_tool ON http_traffic(tool)`,
		`CREATE INDEX IF NOT EXISTS idx_method ON http_traffic(method)`,
		`CREATE INDEX IF NOT EXISTS idx_host_timestamp ON http_traffic(host, timestamp DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_session ON http_traffic(session_tag, timestamp DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_method_url ON http_traffic(method, url)`,
		`CREATE INDEX IF NOT EXISTS idx_request_hash ON http_traffic(request_hash)`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("%w\n  SQL: %s", err, s)
		}
	}
	return tx.Commit()
}

// LogTraffic writes a single traffic record to the project SQLite DB.
// It is designed to be called from a goroutine: go projectDB.LogTraffic(...)
// If no DB is open it returns nil silently.
func (p *ProjectDB) LogTraffic(userdata types.UserData, rawReq, rawResp string) error {
	proj := sanitizeProjectName(userdata.Project)

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

	// ADR-004 E1: make http_traffic a superset of _data. fingerprint feeds the
	// clustering/anomaly tools (computed exactly like the _data path); generated_by
	// is the raw source label; global_seq is a cross-project ordering key
	// (UnixNano — monotonic enough to sort a UNION across project DBs).
	fingerprint := ComputeFingerprint(status, mime, []byte(respBody))
	generatedBy := userdata.GeneratedBy
	globalSeq := time.Now().UnixNano()

	// doInsert writes the row (three inserts in one tx — ADR-003 A3) to a handle.
	doInsert := func(db *sql.DB) error {
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("projectDB.LogTraffic: begin tx: %w", err)
		}
		result, err := tx.Exec(`INSERT INTO http_traffic
			(timestamp, tool, method, host, path, query, param_count, status_code,
			 response_length, protocol, port, url, mime_type, extension, page_title,
			 content_type, request_hash, session_tag, fingerprint, generated_by, global_seq)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			timestamp, tool, method, host, path, query, paramCount, status,
			respLength, protocol, port, fullURL, mime, ext, title, contentType,
			requestHash, "", fingerprint, generatedBy, globalSeq,
		)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("projectDB.LogTraffic: traffic insert failed: %w", err)
		}
		if requestID, idErr := result.LastInsertId(); idErr == nil && requestID != 0 {
			_, _ = tx.Exec(`INSERT OR IGNORE INTO http_messages
				(request_id, request_headers, request_body, response_headers, response_body)
				VALUES (?, ?, ?, ?, ?)`,
				requestID, reqHeaders, []byte(reqBody), respHeaders, []byte(respBody))
			_, _ = tx.Exec(`INSERT INTO traffic_fts (rowid, url, request_headers, request_body, response_headers, response_body)
				VALUES (?, ?, ?, ?, ?, ?)`,
				requestID, fullURL, reqHeaders, reqBody, respHeaders, respBody)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("projectDB.LogTraffic: commit: %w", err)
		}
		return nil
	}

	// Resolve the target handle and insert under ONE read lock so the handle
	// can't be evicted mid-insert (ADR-003 A1). For an addressed project whose
	// handle was evicted between calls, OPEN it and retry — never misroute an
	// addressed write to the active handle (the bug the stress test caught:
	// eviction churn dropped a row). Bounded retries guard against pathological
	// churn (returns an error rather than silently losing the row).
	for attempt := 0; attempt < 16; attempt++ {
		p.mu.RLock()
		if p.db == nil || !p.ready {
			p.mu.RUnlock()
			return nil
		}
		if !trafficLogging.shouldLog(userdata.GeneratedBy) {
			p.mu.RUnlock()
			return nil
		}
		var db *sql.DB
		if proj == "" || proj == p.name {
			db = p.db
		} else if h, ok := p.writeHandles[proj]; ok {
			db = h
		}
		if db != nil {
			err := doInsert(db)
			p.mu.RUnlock()
			return err
		}
		p.mu.RUnlock()

		// Addressed handle not open (first write, or evicted) — open it, retry.
		p.mu.Lock()
		_, oerr := p.writeHandleForLocked(proj)
		p.mu.Unlock()
		if oerr != nil {
			return fmt.Errorf("projectDB.LogTraffic: resolve project %q: %w", proj, oerr)
		}
	}
	return fmt.Errorf("projectDB.LogTraffic: handle for project %q evicted repeatedly under churn", proj)
}

// writeHandleForLocked returns an open read/write *sql.DB for the named project,
// opening and caching it on first use. Caller must hold p.mu. Used by LogTraffic
// to route a captured/sent row to a project OTHER than the Active one without
// changing the Active write target (ADR-002). Empty name (or the Active name)
// resolves to the Active handle. Mirrors openLocked's open/PRAGMA/schema steps.
func (p *ProjectDB) writeHandleForLocked(name string) (*sql.DB, error) {
	sanitized := sanitizeProjectName(name)
	if sanitized == "" || sanitized == p.name {
		return p.db, nil
	}
	if h, ok := p.writeHandles[sanitized]; ok {
		return h, nil
	}

	dir := p.dbDir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("writeHandleForLocked: cannot determine home directory: %w", err)
		}
		dir = home
	}

	dbFile := filepath.Join(dir, sanitized+".db")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("writeHandleForLocked: create db directory %s: %w", dir, err)
	}

	_, existErr := os.Stat(dbFile)
	isNew := os.IsNotExist(existErr)

	db, err := sql.Open("sqlite", dbFile)
	if err != nil {
		return nil, fmt.Errorf("writeHandleForLocked: open %s: %w", dbFile, err)
	}
	// Single writer connection per handle — see openLocked (ADR-003 A1).
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("writeHandleForLocked: set WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("writeHandleForLocked: set busy_timeout: %w", err)
	}
	if err := initProjectSchema(db, isNew); err != nil {
		db.Close()
		return nil, fmt.Errorf("writeHandleForLocked: schema init for %s: %w", dbFile, err)
	}

	if p.writeHandles == nil {
		p.writeHandles = make(map[string]*sql.DB)
	}
	// Evict oldest-opened handles to keep the registry bounded (ADR-003 A2).
	// Safe here: writeHandleForLocked runs under the exclusive lock, so no
	// in-flight LogTraffic (which holds the read lock) is mid-insert.
	for len(p.writeHandles) >= maxWriteHandles && len(p.writeHandleOrder) > 0 {
		evicted := p.writeHandleOrder[0]
		p.closeHandlesForProjectLocked(evicted)
		log.Printf("[ProjectDB] evicted write handle (registry at cap %d): %s", maxWriteHandles, evicted)
	}
	p.writeHandles[sanitized] = db
	p.writeHandleOrder = append(p.writeHandleOrder, sanitized)
	log.Printf("[ProjectDB] opened addressed write handle: %s", dbFile)
	return db, nil
}

// closeHandlesForProjectLocked closes and drops any open addressed write handle
// for the named project. Caller MUST hold p.mu exclusively (so no in-flight
// LogTraffic — which holds the read lock — is mid-insert on the handle). Shared
// by registry eviction (A2) and project delete (B3). No-op for the Active
// handle, which is owned by SetProject/Close, not the registry.
func (p *ProjectDB) closeHandlesForProjectLocked(name string) {
	sanitized := sanitizeProjectName(name)
	if h, ok := p.writeHandles[sanitized]; ok {
		checkpointAndClose(h)
		delete(p.writeHandles, sanitized)
	}
	for i, n := range p.writeHandleOrder {
		if n == sanitized {
			p.writeHandleOrder = append(p.writeHandleOrder[:i], p.writeHandleOrder[i+1:]...)
			break
		}
	}
}

// closeProjectHandle closes any open registry write handle and/or viewer for the
// named project so its files can be deleted (ADR-003 B3). It never touches the
// Active handle — delete refuses the active project upstream (in-use guard).
func (p *ProjectDB) closeProjectHandle(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	sanitized := sanitizeProjectName(name)
	p.closeHandlesForProjectLocked(sanitized)
	if p.viewedName == sanitized && p.viewedDB != nil && p.viewedDB != p.db {
		_ = p.viewedDB.Close()
		p.viewedDB = nil
		p.viewedName = ""
		p.viewedPath = ""
	}
}

// Close closes the current database connection.
func (p *ProjectDB) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.viewedDB != nil && p.viewedDB != p.db {
		_ = p.viewedDB.Close()
	}
	p.viewedDB = nil
	p.viewedName = ""
	p.viewedPath = ""

	// Release any addressed write handles opened by writeHandleForLocked.
	for _, h := range p.writeHandles {
		checkpointAndClose(h)
	}
	p.writeHandles = nil
	p.writeHandleOrder = nil

	if p.db != nil {
		checkpointAndClose(p.db)
		p.db = nil
	}
	p.ready = false
}

// checkpointAndClose flushes a handle's committed WAL frames into the main DB
// before closing it (ADR-004). TRUNCATE reliably moves committed frames into the
// main DB so a later reader sees them even if the -wal is later orphaned — this
// fixed the lost-write the stress test caught (PASSIVE left too many frames
// behind under churn). Combined with openProjectRO (WAL-recoverable), realistic
// workloads capture exactly; only -race timing leaves a rare residual.
func checkpointAndClose(h *sql.DB) {
	_, _ = h.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	_ = h.Close()
}

// SetViewed opens a separate read-only handle on a different project DB so
// the UI can browse it without disturbing the Active write target. Calling
// with the Active project's name (or empty name) clears any prior viewer
// and falls back to the Active handle.
//
// Subsequent UI reads should route through ViewedDB(); writes still use
// the Active handle (db) so they remain "sticky" to the project the user
// is actually working on.
func (p *ProjectDB) SetViewed(name string, dbDir string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	sanitized := sanitizeProjectName(name)

	// Empty name or pointing at Active = clear viewer.
	if sanitized == "" || sanitized == p.name {
		if p.viewedDB != nil && p.viewedDB != p.db {
			_ = p.viewedDB.Close()
		}
		p.viewedDB = nil
		p.viewedName = ""
		p.viewedPath = ""
		return nil
	}

	dir := dbDir
	if dir == "" {
		dir = p.dbDir
	}
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("projectDB.SetViewed: cannot determine home directory: %w", err)
		}
		dir = home
	}

	dbFile := filepath.Join(dir, sanitized+".db")
	if _, err := os.Stat(dbFile); err != nil {
		return fmt.Errorf("projectDB.SetViewed: %s not found at %s", sanitized, dbFile)
	}

	// Read-only attach. mode=ro + immutable=0 keeps the WAL writer (the
	// Active proxy goroutines) from being blocked. _query_only further
	// rejects any DML statements that slip through a misclassified handler.
	dsn := fmt.Sprintf("file:%s?mode=ro&_query_only=on&_busy_timeout=5000", dbFile)
	newDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("projectDB.SetViewed: open %s: %w", dbFile, err)
	}
	if err := newDB.Ping(); err != nil {
		_ = newDB.Close()
		return fmt.Errorf("projectDB.SetViewed: ping %s: %w", dbFile, err)
	}

	// Swap & close the old viewer (if any).
	if p.viewedDB != nil && p.viewedDB != p.db {
		_ = p.viewedDB.Close()
	}
	p.viewedDB = newDB
	p.viewedName = sanitized
	p.viewedPath = dbFile

	log.Printf("[ProjectDB] viewer opened: %s", dbFile)
	return nil
}

// ClearViewed drops any read-only viewer so subsequent UI reads route
// through the Active handle. Equivalent to SetViewed("", "").
func (p *ProjectDB) ClearViewed() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.viewedDB != nil && p.viewedDB != p.db {
		_ = p.viewedDB.Close()
	}
	p.viewedDB = nil
	p.viewedName = ""
	p.viewedPath = ""
}

// ViewedDB returns the handle UI read endpoints should use. Falls back to
// the Active handle when no viewer is set.
func (p *ProjectDB) ViewedDB() *sql.DB {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.viewedDB != nil {
		return p.viewedDB
	}
	return p.db
}

// UIRead atomically returns the handle and project name UI read endpoints
// should use, plus a "ready" flag. When a separate viewer is set it returns
// (viewedDB, viewedName, true); otherwise (db, name, ready). Used by
// traffic_list / traffic_detail / etc. so they grab a single coherent
// snapshot under the mutex instead of three separate field reads.
func (p *ProjectDB) UIRead() (db *sql.DB, name string, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.viewedDB != nil {
		return p.viewedDB, p.viewedName, true
	}
	return p.db, p.name, p.ready
}

// IsViewingActive reports whether the UI is currently looking at the
// Active project (i.e. no separate viewer set). UI write handlers should
// gate on this — return 409 when false so writes can't accidentally
// target a project the user is just browsing.
func (p *ProjectDB) IsViewingActive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.viewedDB == nil || p.viewedDB == p.db
}

// ActiveAndViewedNames returns a snapshot of the Active and Viewed names
// under a single mutex acquisition. Used by the write-gate to populate
// 409 responses with both names without racing.
func (p *ProjectDB) ActiveAndViewedNames() (active, viewed string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v := p.viewedName
	if v == "" {
		v = p.name
	}
	return p.name, v
}

// ViewedName returns the name of the project currently being viewed,
// falling back to the Active name when no separate viewer is set.
func (p *ProjectDB) ViewedName() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.viewedName != "" {
		return p.viewedName
	}
	return p.name
}

// ---------------------------------------------------------------------------
// readDBCache: per-call read-only handles for MCP tools that take a
// `project` arg. Kept separate from the UI's viewedDB so an LLM can scope
// reads per-call without affecting what the user has selected on screen.
// ---------------------------------------------------------------------------

type readDBCacheEntry struct {
	db   *sql.DB
	path string
}

var readDBCache = struct {
	mu      sync.Mutex
	entries map[string]readDBCacheEntry // key = absolute dbPath
}{entries: make(map[string]readDBCacheEntry)}

// resolveReadDB returns a read-only sql.DB for the given project name.
// Empty name → Active handle (the default). When a name is provided that
// matches Active, returns Active. Otherwise opens (or reuses) a cached
// read-only handle to <dbDir>/<name>.db.
//
// Callers must NOT close the returned handle.
func resolveReadDB(name string) (*sql.DB, error) {
	if name == "" {
		return projectDB.db, nil
	}
	sanitized := sanitizeProjectName(name)
	if sanitized == "" {
		return projectDB.db, nil
	}

	projectDB.mu.Lock()
	if sanitized == projectDB.name {
		db := projectDB.db
		projectDB.mu.Unlock()
		return db, nil
	}
	dbDir := projectDB.dbDir
	projectDB.mu.Unlock()

	if dbDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolveReadDB: cannot determine home directory: %w", err)
		}
		dbDir = home
	}
	dbFile := filepath.Join(dbDir, sanitized+".db")

	readDBCache.mu.Lock()
	defer readDBCache.mu.Unlock()
	if entry, ok := readDBCache.entries[dbFile]; ok {
		return entry.db, nil
	}

	if _, err := os.Stat(dbFile); err != nil {
		return nil, fmt.Errorf("resolveReadDB: project %q not found at %s", sanitized, dbFile)
	}

	dsn := fmt.Sprintf("file:%s?mode=ro&_query_only=on&_busy_timeout=5000", dbFile)
	newDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("resolveReadDB: open %s: %w", dbFile, err)
	}
	if err := newDB.Ping(); err != nil {
		_ = newDB.Close()
		return nil, fmt.Errorf("resolveReadDB: ping %s: %w", dbFile, err)
	}

	readDBCache.entries[dbFile] = readDBCacheEntry{db: newDB, path: dbFile}
	log.Printf("[ProjectDB] readDBCache: opened %s", dbFile)
	return newDB, nil
}

// Info returns a snapshot of the current project state. The "viewed"
// fields describe what the UI is currently reading from; when no separate
// viewer is set they mirror the active fields. UI clients can compare
// activeName vs viewedName to decide whether to show the read-only banner.
func (p *ProjectDB) Info() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()

	viewedName := p.viewedName
	viewedPath := p.viewedPath
	viewing := p.viewedDB
	if viewedName == "" {
		viewedName = p.name
		viewedPath = p.dbPath
		viewing = p.db
	}

	info := map[string]any{
		// Legacy fields (kept so existing callers don't break).
		"projectName":  p.name,
		"dbPath":       p.dbPath,
		"dbDir":        p.dbDir,
		"isActive":     p.ready,
		"trafficCount": 0,

		// Active vs viewed split.
		"activeName":      p.name,
		"activePath":      p.dbPath,
		"viewedName":      viewedName,
		"viewedPath":      viewedPath,
		"isViewingActive": p.viewedDB == nil || p.viewedDB == p.db,
	}

	if viewing != nil {
		var count int
		if err := viewing.QueryRow("SELECT count(*) FROM http_traffic").Scan(&count); err == nil {
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

	// Mirror the project name into the lorgdb _settings table for the UI and other tools.
	backend.storeProjectName(sanitized)

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

	// trafficCount is already set by Info() from the per-project DB.
	// Compute hostCount from the same source so both numbers are consistent.
	projectDB.mu.Lock()
	activeDB := projectDB.db
	projectDB.mu.Unlock()
	if activeDB != nil {
		var hostCount int
		if err := activeDB.QueryRow("SELECT COUNT(DISTINCT host) FROM http_traffic").Scan(&hostCount); err == nil {
			info["hostCount"] = hostCount
		}
	}

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

	// Mirror the project name into the lorgdb _settings table for the UI and other tools.
	backend.storeProjectName(sanitizeProjectName(args.Name))

	return mcpJSONResult(map[string]any{
		"success":     true,
		"projectName": sanitizeProjectName(args.Name),
		"dbPath":      projectDB.dbPath,
	})
}

// storeProjectName persists the project name in the lorgdb _settings table
// for the UI and other tools.
func (backend *Backend) storeProjectName(name string) {
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
	Enabled bool     `json:"enabled" jsonschema:"required" jsonschema_description:"Enable or disable traffic capture. When false, no traffic is written to either the global store or the per-project mirror."`
	Sources []string `json:"sources,omitempty" jsonschema_description:"Which sources to gate: proxy, repeater, mcp, template, all (default: all). Affects both global and per-project writes."`
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
// projectExport handler -- full export from lorgdb for reimporting old data
// ---------------------------------------------------------------------------

// parseJSONField handles JSON fields that may be stored as text strings,
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
    request_hash  TEXT,
    session_tag   TEXT,
    notes         TEXT,
    fingerprint   TEXT    NOT NULL DEFAULT '',
    generated_by  TEXT    NOT NULL DEFAULT '',
    global_seq    INTEGER NOT NULL DEFAULT 0
)`,
	`CREATE INDEX idx_ht_fingerprint ON http_traffic(fingerprint)`,
	`CREATE INDEX idx_ht_global_seq ON http_traffic(global_seq)`,
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
	`CREATE INDEX idx_request_hash ON http_traffic(request_hash)`,
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
		created := rec.GetString("created") // lorgdb timestamp string

		// Parse req_json and resp_json -- lorgdb may return types.JsonRaw, string, or map
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

		// Use stored created timestamp, fallback to now
		timestamp := created
		if timestamp == "" {
			timestamp = time.Now().UTC().Format(time.RFC3339)
		}

		// Insert traffic record
		result, err := txTraffic.Exec(
			timestamp,   // timestamp
			tool,        // tool
			method,      // method
			exportHost,  // host
			path,        // path
			query,       // query
			paramCount,  // param_count
			status,      // status_code
			respLength,  // response_length
			protocol,    // protocol
			port,        // port
			fullURL,     // url
			mime,        // mime_type
			ext,         // extension
			title,       // page_title
			contentType, // content_type
			requestHash, // request_hash
			"",          // session_tag
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
						"name":             name,
						"tls":              tr.GetBool("tls"),
						"host":             tr.GetString("host"),
						"port":             tr.GetFloat("port"),
						"http_version":     tr.GetFloat("http_version"),
						"request_template": tr.GetString("request_template"),
						"variables":        tr.Get("variables"),
						"description":      tr.GetString("description"),
						"inject_session":   tr.GetBool("inject_session"),
						"json_escape_vars": tr.GetBool("json_escape_vars"),
						"extract_regex":    tr.GetString("extract_regex"),
						"extract_group":    tr.GetFloat("extract_group"),
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
// tool name for the export. AI/MCP traffic gets routed through the
// repeater (so its generated_by reads "repeater/ai/mcp/..."), so the
// substring check for "ai/mcp" must run BEFORE the "repeater/" prefix
// check or AI traffic gets mislabeled as plain "Repeater" — which the
// UI then collapses to "Proxy".
func mapGeneratedByToTool(generatedBy string) string {
	switch {
	case strings.Contains(generatedBy, "ai/mcp"):
		return "MCP"
	case strings.HasPrefix(generatedBy, "proxy/"):
		return "Proxy"
	case strings.Contains(generatedBy, "template"):
		return "Template"
	case strings.HasPrefix(generatedBy, "repeater/"):
		return "Repeater"
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
