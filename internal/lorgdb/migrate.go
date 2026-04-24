package lorgdb

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"log"
	"math/big"
	"strings"
)

// Migration represents a single schema migration step.
type Migration struct {
	Version     int
	Description string
	Up          func(db *sql.DB) error
}

// Migrations is the ordered list of all migrations.
// Migration 0 is a no-op for existing PocketBase databases.
// Migration 1 creates all tables for fresh installs.
var Migrations = []Migration{
	{
		Version:     0,
		Description: "no-op for existing PocketBase databases",
		Up:          func(db *sql.DB) error { return nil },
	},
	{
		Version:     1,
		Description: "create all tables and indexes for fresh installs",
		Up:          migrationCreateAllTables,
	},
	{
		Version:     2,
		Description: "add notes column to _hosts",
		Up: func(db *sql.DB) error {
			_, err := db.Exec("ALTER TABLE _hosts ADD COLUMN notes JSON DEFAULT NULL")
			if err != nil && !strings.Contains(err.Error(), "duplicate column") {
				return err
			}
			return nil
		},
	},
	{
		Version:     3,
		Description: "add upstream proxy, mTLS, and match/replace support",
		Up: func(db *sql.DB) error {
			// Add upstream proxy and mTLS fields to _proxies
			alters := []string{
				"ALTER TABLE _proxies ADD COLUMN upstream_proxy TEXT NOT NULL DEFAULT ''",
				"ALTER TABLE _proxies ADD COLUMN client_cert TEXT NOT NULL DEFAULT ''",
				"ALTER TABLE _proxies ADD COLUMN client_key TEXT NOT NULL DEFAULT ''",
			}
			for _, stmt := range alters {
				if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
					return err
				}
			}

			// Create match & replace rules table
			_, err := db.Exec(`CREATE TABLE IF NOT EXISTS _match_replace (
				id       TEXT PRIMARY KEY NOT NULL,
				created  TEXT NOT NULL DEFAULT '',
				updated  TEXT NOT NULL DEFAULT '',
				enabled  BOOLEAN NOT NULL DEFAULT TRUE,
				type     TEXT NOT NULL DEFAULT '',
				match    TEXT NOT NULL DEFAULT '',
				replace  TEXT NOT NULL DEFAULT '',
				scope    TEXT NOT NULL DEFAULT '',
				comment  TEXT NOT NULL DEFAULT '',
				priority REAL NOT NULL DEFAULT 0
			)`)
			return err
		},
	},
	{
		Version:     4,
		Description: "add fingerprint column to _data for response clustering",
		Up: func(db *sql.DB) error {
			alters := []string{
				"ALTER TABLE _data ADD COLUMN fingerprint TEXT NOT NULL DEFAULT ''",
			}
			for _, stmt := range alters {
				if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
					return err
				}
			}
			if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_data_fingerprint ON _data (fingerprint)`); err != nil {
				return err
			}
			return nil
		},
	},
}

// RunMigrations applies any unapplied migrations in order.
func (d *LorgDB) RunMigrations() error {
	// Ensure the migrations tracking table exists.
	if _, err := d.db.Exec(`
		CREATE TABLE IF NOT EXISTS _lorg_migrations (
			version  INTEGER PRIMARY KEY,
			applied  TEXT NOT NULL DEFAULT (datetime('now'))
		)
	`); err != nil {
		return fmt.Errorf("lorgdb: create _lorg_migrations: %w", err)
	}

	for _, m := range Migrations {
		var exists int
		err := d.db.QueryRow("SELECT 1 FROM _lorg_migrations WHERE version = ?", m.Version).Scan(&exists)
		if err == nil {
			continue // already applied
		}
		if err != sql.ErrNoRows {
			return fmt.Errorf("lorgdb: check migration %d: %w", m.Version, err)
		}

		log.Printf("[LorgDB] Applying migration %d: %s", m.Version, m.Description)
		if err := m.Up(d.db); err != nil {
			return fmt.Errorf("lorgdb: migration %d failed: %w", m.Version, err)
		}

		if _, err := d.db.Exec("INSERT INTO _lorg_migrations (version) VALUES (?)", m.Version); err != nil {
			return fmt.Errorf("lorgdb: record migration %d: %w", m.Version, err)
		}
	}

	return nil
}

// migrationCreateAllTables creates every table and index needed for a fresh
// lorg install. Uses IF NOT EXISTS so it's safe on existing PocketBase DBs.
func migrationCreateAllTables(db *sql.DB) error {
	for _, stmt := range allTableSQL {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("statement failed: %w\n  SQL: %.200s", err, stmt)
		}
	}
	return nil
}

// allTableSQL is the complete set of CREATE TABLE / CREATE INDEX statements
// matching the PocketBase schema. PocketBase tables always have id (TEXT PK),
// created (TEXT), updated (TEXT) plus the schema-defined columns.
//
// Column types follow PocketBase conventions:
//   - Text / Editor / File / Relation → TEXT DEFAULT '' NOT NULL
//   - Number                          → REAL DEFAULT 0 NOT NULL
//   - Bool                            → BOOLEAN DEFAULT FALSE NOT NULL
//   - Json                            → JSON DEFAULT NULL
//   - Date                            → TEXT DEFAULT '' NOT NULL
var allTableSQL = []string{
	// -----------------------------------------------------------------------
	// Core traffic tables (from schemas.Collections)
	// -----------------------------------------------------------------------

	`CREATE TABLE IF NOT EXISTS _req (
		id      TEXT PRIMARY KEY NOT NULL,
		created TEXT NOT NULL DEFAULT '',
		updated TEXT NOT NULL DEFAULT '',
		method      TEXT NOT NULL DEFAULT '',
		url         TEXT NOT NULL DEFAULT '',
		path        TEXT NOT NULL DEFAULT '',
		query       TEXT NOT NULL DEFAULT '',
		fragment    TEXT NOT NULL DEFAULT '',
		ext         TEXT NOT NULL DEFAULT '',
		has_cookies BOOLEAN NOT NULL DEFAULT FALSE,
		length      REAL NOT NULL DEFAULT 0,
		headers     JSON DEFAULT NULL,
		raw         TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_req_method ON _req (method)`,

	`CREATE TABLE IF NOT EXISTS _resp (
		id      TEXT PRIMARY KEY NOT NULL,
		created TEXT NOT NULL DEFAULT '',
		updated TEXT NOT NULL DEFAULT '',
		title       TEXT NOT NULL DEFAULT '',
		mime        TEXT NOT NULL DEFAULT '',
		status      REAL NOT NULL DEFAULT 0,
		length      REAL NOT NULL DEFAULT 0,
		has_cookies BOOLEAN NOT NULL DEFAULT FALSE,
		headers     JSON DEFAULT NULL,
		raw         TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_resp_status ON _resp (status)`,

	`CREATE TABLE IF NOT EXISTS _req_edited (
		id      TEXT PRIMARY KEY NOT NULL,
		created TEXT NOT NULL DEFAULT '',
		updated TEXT NOT NULL DEFAULT '',
		method      TEXT NOT NULL DEFAULT '',
		url         TEXT NOT NULL DEFAULT '',
		path        TEXT NOT NULL DEFAULT '',
		query       TEXT NOT NULL DEFAULT '',
		fragment    TEXT NOT NULL DEFAULT '',
		ext         TEXT NOT NULL DEFAULT '',
		has_cookies BOOLEAN NOT NULL DEFAULT FALSE,
		length      REAL NOT NULL DEFAULT 0,
		headers     JSON DEFAULT NULL,
		raw         TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_req_edited_method ON _req_edited (method)`,

	`CREATE TABLE IF NOT EXISTS _resp_edited (
		id      TEXT PRIMARY KEY NOT NULL,
		created TEXT NOT NULL DEFAULT '',
		updated TEXT NOT NULL DEFAULT '',
		title       TEXT NOT NULL DEFAULT '',
		mime        TEXT NOT NULL DEFAULT '',
		status      REAL NOT NULL DEFAULT 0,
		length      REAL NOT NULL DEFAULT 0,
		has_cookies BOOLEAN NOT NULL DEFAULT FALSE,
		headers     JSON DEFAULT NULL,
		raw         TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_resp_edited_status ON _resp_edited (status)`,

	`CREATE TABLE IF NOT EXISTS _data (
		id      TEXT PRIMARY KEY NOT NULL,
		created TEXT NOT NULL DEFAULT '',
		updated TEXT NOT NULL DEFAULT '',
		project         TEXT NOT NULL DEFAULT '',
		"index"         REAL NOT NULL DEFAULT 0,
		index_minor     REAL NOT NULL DEFAULT 0,
		host            TEXT NOT NULL DEFAULT '',
		port            TEXT NOT NULL DEFAULT '',
		has_params      BOOLEAN NOT NULL DEFAULT FALSE,
		has_resp        BOOLEAN NOT NULL DEFAULT FALSE,
		is_https        BOOLEAN NOT NULL DEFAULT FALSE,
		http            TEXT NOT NULL DEFAULT '',
		proxy_id        TEXT NOT NULL DEFAULT '',
		is_req_edited   BOOLEAN NOT NULL DEFAULT FALSE,
		is_resp_edited  BOOLEAN NOT NULL DEFAULT FALSE,
		req             TEXT NOT NULL DEFAULT '',
		resp            TEXT NOT NULL DEFAULT '',
		req_edited      TEXT NOT NULL DEFAULT '',
		resp_edited     TEXT NOT NULL DEFAULT '',
		req_json        JSON DEFAULT NULL,
		resp_json       JSON DEFAULT NULL,
		req_edited_json JSON DEFAULT NULL,
		resp_edited_json JSON DEFAULT NULL,
		generated_by    TEXT NOT NULL DEFAULT '',
		extra           JSON DEFAULT NULL,
		attached        TEXT NOT NULL DEFAULT '',
		action          TEXT NOT NULL DEFAULT '',
		fingerprint     TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_data_generated_by ON _data (generated_by)`,
	`CREATE INDEX IF NOT EXISTS idx_data_fingerprint ON _data (fingerprint)`,

	`CREATE TABLE IF NOT EXISTS _proxies (
		id      TEXT PRIMARY KEY NOT NULL,
		created TEXT NOT NULL DEFAULT '',
		updated TEXT NOT NULL DEFAULT '',
		project   TEXT NOT NULL DEFAULT '',
		label     TEXT NOT NULL DEFAULT '',
		addr      TEXT NOT NULL DEFAULT '',
		browser   TEXT NOT NULL DEFAULT '',
		intercept BOOLEAN NOT NULL DEFAULT FALSE,
		state     TEXT NOT NULL DEFAULT '',
		color     TEXT NOT NULL DEFAULT '',
		profile   TEXT NOT NULL DEFAULT '',
		data      JSON DEFAULT NULL,
		upstream_proxy TEXT NOT NULL DEFAULT '',
		client_cert    TEXT NOT NULL DEFAULT '',
		client_key     TEXT NOT NULL DEFAULT ''
	)`,

	`CREATE TABLE IF NOT EXISTS _labels (
		id      TEXT PRIMARY KEY NOT NULL,
		created TEXT NOT NULL DEFAULT '',
		updated TEXT NOT NULL DEFAULT '',
		name  TEXT NOT NULL DEFAULT '',
		color TEXT NOT NULL DEFAULT '',
		icon  TEXT NOT NULL DEFAULT '',
		type  TEXT NOT NULL DEFAULT '',
		extra JSON DEFAULT NULL
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_labelsname ON _labels (name)`,

	`CREATE TABLE IF NOT EXISTS _searches (
		id      TEXT PRIMARY KEY NOT NULL,
		created TEXT NOT NULL DEFAULT '',
		updated TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL DEFAULT '',
		data JSON DEFAULT NULL
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_searches_name ON _searches (name)`,

	`CREATE TABLE IF NOT EXISTS _filters (
		id      TEXT PRIMARY KEY NOT NULL,
		created TEXT NOT NULL DEFAULT '',
		updated TEXT NOT NULL DEFAULT '',
		name   TEXT NOT NULL DEFAULT '',
		filter TEXT NOT NULL DEFAULT '',
		data   JSON DEFAULT NULL
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_filters_name ON _filters (name)`,

	`CREATE TABLE IF NOT EXISTS _wordlists (
		id      TEXT PRIMARY KEY NOT NULL,
		created TEXT NOT NULL DEFAULT '',
		updated TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL DEFAULT '',
		path TEXT NOT NULL DEFAULT '',
		data JSON DEFAULT NULL
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_wordlists_name ON _wordlists (name)`,

	`CREATE TABLE IF NOT EXISTS _playground (
		id      TEXT PRIMARY KEY NOT NULL,
		created TEXT NOT NULL DEFAULT '',
		updated TEXT NOT NULL DEFAULT '',
		name            TEXT NOT NULL DEFAULT '',
		parent_id       TEXT NOT NULL DEFAULT '',
		original_req_id TEXT NOT NULL DEFAULT '',
		type            TEXT NOT NULL DEFAULT '',
		expanded        BOOLEAN NOT NULL DEFAULT FALSE,
		state           TEXT NOT NULL DEFAULT '',
		sort_order      REAL NOT NULL DEFAULT 0,
		data            JSON DEFAULT NULL,
		extra           JSON DEFAULT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS _tech (
		id      TEXT PRIMARY KEY NOT NULL,
		created TEXT NOT NULL DEFAULT '',
		updated TEXT NOT NULL DEFAULT '',
		name     TEXT NOT NULL DEFAULT '',
		image    TEXT NOT NULL DEFAULT '',
		color    TEXT NOT NULL DEFAULT '',
		category TEXT NOT NULL DEFAULT '',
		extra    JSON DEFAULT NULL
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_techname ON _tech (name)`,

	`CREATE TABLE IF NOT EXISTS _intercept (
		id      TEXT PRIMARY KEY NOT NULL,
		created TEXT NOT NULL DEFAULT '',
		updated TEXT NOT NULL DEFAULT '',
		"index"         REAL NOT NULL DEFAULT 0,
		host            TEXT NOT NULL DEFAULT '',
		port            TEXT NOT NULL DEFAULT '',
		has_params      BOOLEAN NOT NULL DEFAULT FALSE,
		has_resp        BOOLEAN NOT NULL DEFAULT FALSE,
		is_https        BOOLEAN NOT NULL DEFAULT FALSE,
		is_req_edited   BOOLEAN NOT NULL DEFAULT FALSE,
		is_resp_edited  BOOLEAN NOT NULL DEFAULT FALSE,
		req             TEXT NOT NULL DEFAULT '',
		resp            TEXT NOT NULL DEFAULT '',
		req_edited      TEXT NOT NULL DEFAULT '',
		resp_edited     TEXT NOT NULL DEFAULT '',
		req_json        JSON DEFAULT NULL,
		resp_json       JSON DEFAULT NULL,
		req_edited_json JSON DEFAULT NULL,
		resp_edited_json JSON DEFAULT NULL,
		attached        TEXT NOT NULL DEFAULT '',
		generated_by    TEXT NOT NULL DEFAULT '',
		extra           JSON DEFAULT NULL,
		action          TEXT NOT NULL DEFAULT ''
	)`,

	`CREATE TABLE IF NOT EXISTS _hosts (
		id      TEXT PRIMARY KEY NOT NULL,
		created TEXT NOT NULL DEFAULT '',
		updated TEXT NOT NULL DEFAULT '',
		host      TEXT NOT NULL DEFAULT '',
		smartsort TEXT NOT NULL DEFAULT '',
		domain    TEXT NOT NULL DEFAULT '',
		title     TEXT NOT NULL DEFAULT '',
		status    TEXT NOT NULL DEFAULT '',
		favicon   TEXT NOT NULL DEFAULT '',
		tech      TEXT NOT NULL DEFAULT '',
		notes     JSON DEFAULT NULL,
		extra     JSON DEFAULT NULL
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_hosts_name ON _hosts (host)`,

	`CREATE TABLE IF NOT EXISTS _settings (
		id      TEXT PRIMARY KEY NOT NULL,
		created TEXT NOT NULL DEFAULT '',
		updated TEXT NOT NULL DEFAULT '',
		option TEXT NOT NULL DEFAULT '',
		value  TEXT NOT NULL DEFAULT ''
	)`,

	`CREATE TABLE IF NOT EXISTS _configs (
		id      TEXT PRIMARY KEY NOT NULL,
		created TEXT NOT NULL DEFAULT '',
		updated TEXT NOT NULL DEFAULT '',
		key  TEXT NOT NULL DEFAULT '',
		data JSON DEFAULT NULL
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_configs_key ON _configs (key)`,

	`CREATE TABLE IF NOT EXISTS _processes (
		id      TEXT PRIMARY KEY NOT NULL,
		created TEXT NOT NULL DEFAULT '',
		updated TEXT NOT NULL DEFAULT '',
		name         TEXT NOT NULL DEFAULT '',
		description  TEXT NOT NULL DEFAULT '',
		type         TEXT NOT NULL DEFAULT '',
		data         JSON DEFAULT NULL,
		input        JSON DEFAULT NULL,
		output       JSON DEFAULT NULL,
		state        TEXT NOT NULL DEFAULT '',
		parent_id    TEXT NOT NULL DEFAULT '',
		generated_by TEXT NOT NULL DEFAULT '',
		created_by   TEXT NOT NULL DEFAULT ''
	)`,

	`CREATE TABLE IF NOT EXISTS _ui (
		id      TEXT PRIMARY KEY NOT NULL,
		created TEXT NOT NULL DEFAULT '',
		updated TEXT NOT NULL DEFAULT '',
		unique_id TEXT NOT NULL DEFAULT '',
		data      JSON DEFAULT NULL
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_ui_id ON _ui (unique_id)`,

	`CREATE TABLE IF NOT EXISTS _attached (
		id      TEXT PRIMARY KEY NOT NULL,
		created TEXT NOT NULL DEFAULT '',
		updated TEXT NOT NULL DEFAULT '',
		labels TEXT NOT NULL DEFAULT '',
		note   TEXT NOT NULL DEFAULT '',
		extra  JSON DEFAULT NULL
	)`,

	// -----------------------------------------------------------------------
	// Tables added by later PocketBase migrations
	// -----------------------------------------------------------------------

	// _counters (migration 1766447171)
	`CREATE TABLE IF NOT EXISTS _counters (
		id      TEXT PRIMARY KEY NOT NULL,
		created TEXT NOT NULL DEFAULT '',
		updated TEXT NOT NULL DEFAULT '',
		counter_key     TEXT NOT NULL DEFAULT '',
		collection      TEXT NOT NULL DEFAULT '',
		filter          TEXT NOT NULL DEFAULT '',
		count           REAL NOT NULL DEFAULT 0,
		load_on_startup BOOLEAN NOT NULL DEFAULT FALSE
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_counters_key ON _counters (counter_key)`,

	// _websockets (migration 1769215000)
	`CREATE TABLE IF NOT EXISTS _websockets (
		id      TEXT PRIMARY KEY NOT NULL,
		created TEXT NOT NULL DEFAULT '',
		updated TEXT NOT NULL DEFAULT '',
		"index"      REAL NOT NULL DEFAULT 0,
		host         TEXT NOT NULL DEFAULT '',
		path         TEXT NOT NULL DEFAULT '',
		url          TEXT NOT NULL DEFAULT '',
		direction    TEXT NOT NULL DEFAULT '',
		type         TEXT NOT NULL DEFAULT '',
		is_binary    BOOLEAN NOT NULL DEFAULT FALSE,
		payload      TEXT NOT NULL DEFAULT '',
		length       REAL NOT NULL DEFAULT 0,
		timestamp    TEXT NOT NULL DEFAULT '',
		proxy_id     TEXT NOT NULL DEFAULT '',
		data_index   TEXT NOT NULL DEFAULT '',
		generated_by TEXT NOT NULL DEFAULT ''
	)`,

	// _sessions (migration 1771500000)
	`CREATE TABLE IF NOT EXISTS _sessions (
		id      TEXT PRIMARY KEY NOT NULL,
		created TEXT NOT NULL DEFAULT '',
		updated TEXT NOT NULL DEFAULT '',
		name       TEXT NOT NULL DEFAULT '',
		cookies    JSON DEFAULT NULL,
		headers    JSON DEFAULT NULL,
		csrf_token TEXT NOT NULL DEFAULT '',
		csrf_field TEXT NOT NULL DEFAULT '',
		active     BOOLEAN NOT NULL DEFAULT FALSE
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_name ON _sessions (name)`,

	// _mcp_templates (migration 1771500000)
	`CREATE TABLE IF NOT EXISTS _mcp_templates (
		id      TEXT PRIMARY KEY NOT NULL,
		created TEXT NOT NULL DEFAULT '',
		updated TEXT NOT NULL DEFAULT '',
		name             TEXT NOT NULL DEFAULT '',
		tls              BOOLEAN NOT NULL DEFAULT FALSE,
		host             TEXT NOT NULL DEFAULT '',
		port             REAL NOT NULL DEFAULT 0,
		http_version     REAL NOT NULL DEFAULT 0,
		request_template TEXT NOT NULL DEFAULT '',
		variables        JSON DEFAULT NULL,
		description      TEXT NOT NULL DEFAULT '',
		inject_session   BOOLEAN NOT NULL DEFAULT FALSE,
		json_escape_vars BOOLEAN NOT NULL DEFAULT FALSE,
		extract_regex    TEXT NOT NULL DEFAULT '',
		extract_group    REAL NOT NULL DEFAULT 0
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_mcp_templates_name ON _mcp_templates (name)`,

	// -----------------------------------------------------------------------
	// Extra tables not in schemas.Collections but referenced in code
	// -----------------------------------------------------------------------

	`CREATE TABLE IF NOT EXISTS _projects (
		id      TEXT PRIMARY KEY NOT NULL,
		created TEXT NOT NULL DEFAULT '',
		updated TEXT NOT NULL DEFAULT '',
		name    TEXT NOT NULL DEFAULT '',
		path    TEXT NOT NULL DEFAULT '',
		data    JSON DEFAULT NULL,
		version TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_name ON _projects (name)`,

	`CREATE TABLE IF NOT EXISTS _match_replace (
		id       TEXT PRIMARY KEY NOT NULL,
		created  TEXT NOT NULL DEFAULT '',
		updated  TEXT NOT NULL DEFAULT '',
		enabled  BOOLEAN NOT NULL DEFAULT TRUE,
		type     TEXT NOT NULL DEFAULT '',
		match    TEXT NOT NULL DEFAULT '',
		replace  TEXT NOT NULL DEFAULT '',
		scope    TEXT NOT NULL DEFAULT '',
		comment  TEXT NOT NULL DEFAULT '',
		priority REAL NOT NULL DEFAULT 0
	)`,

	`CREATE TABLE IF NOT EXISTS _tools (
		id      TEXT PRIMARY KEY NOT NULL,
		created TEXT NOT NULL DEFAULT '',
		updated TEXT NOT NULL DEFAULT '',
		name  TEXT NOT NULL DEFAULT '',
		path  TEXT NOT NULL DEFAULT '',
		host  TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL DEFAULT '',
		creds JSON DEFAULT NULL,
		data  JSON DEFAULT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS _payloads (
		id      TEXT PRIMARY KEY NOT NULL,
		created TEXT NOT NULL DEFAULT '',
		updated TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL DEFAULT '',
		data JSON DEFAULT NULL
	)`,

	// -----------------------------------------------------------------------
	// Default data for fresh installs
	// -----------------------------------------------------------------------
}

// SeedDefaults inserts default settings, labels, and their label_* collections
// for a fresh database. Safe to call on an existing DB — skips if data exists.
func (d *LorgDB) SeedDefaults() error {
	// Check if default settings already exist.
	var count int
	if err := d.db.QueryRow("SELECT count(*) FROM _settings").Scan(&count); err == nil && count > 0 {
		return nil // already seeded
	}

	// Default settings
	settings := []struct{ id, option, value string }{
		{"PROJECT_NAME__", "Project Name", "Untitled Project"},
		{"PROXY__________", "Proxy", "127.0.0.1:8080"},
		{"INTERCEPT______", "Intercept", "false"},
		{"MAIN_TAB_______", "Main Tab", "Sitemaps"},
	}

	now := "2025-01-01 00:00:00.000Z"
	for _, s := range settings {
		_, err := d.db.Exec(
			"INSERT OR IGNORE INTO _settings (id, created, updated, option, value) VALUES (?, ?, ?, ?, ?)",
			s.id, now, now, s.option, s.value,
		)
		if err != nil {
			return fmt.Errorf("lorgdb: seed setting %s: %w", s.id, err)
		}
	}

	// Default labels
	type label struct{ name, color, typ string }
	labels := []label{
		{"!high", "red", "mark"},
		{"!medium", "orange", "mark"},
		{"!low", "yellow", "mark"},
		{"!info", "ignore", "mark"},
		{"!leak", "violet", "mark"},
		{"interesting", "yellow", "custom"},
		{"weird", "purple", "custom"},
		{"^dummy/folder", "blue", "folder"},
		{"^target/reset", "blue", "folder"},
	}

	for _, l := range labels {
		id := randomID()
		_, err := d.db.Exec(
			"INSERT OR IGNORE INTO _labels (id, created, updated, name, color, icon, type, extra) VALUES (?, ?, ?, ?, ?, '', ?, NULL)",
			id, now, now, l.name, l.color, l.typ,
		)
		if err != nil {
			return fmt.Errorf("lorgdb: seed label %s: %w", l.name, err)
		}

		// Each label gets a label_<id> junction table.
		_, err = d.db.Exec(fmt.Sprintf(`CREATE TABLE IF NOT EXISTS "label_%s" (
			id      TEXT PRIMARY KEY NOT NULL,
			created TEXT NOT NULL DEFAULT '',
			updated TEXT NOT NULL DEFAULT '',
			data  TEXT NOT NULL DEFAULT '',
			extra JSON DEFAULT NULL
		)`, id))
		if err != nil {
			return fmt.Errorf("lorgdb: create label_%s table: %w", id, err)
		}
	}

	log.Printf("[LorgDB] Seeded default settings and labels")
	return nil
}

// randomID generates a 15-character alphanumeric ID matching PocketBase's
// format, using crypto/rand.
func randomID() string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 15)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			panic(err)
		}
		b[i] = alphabet[n.Int64()]
	}
	return string(b)
}
