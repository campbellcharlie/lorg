package app

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/campbellcharlie/lorg/internal/types"
	_ "modernc.org/sqlite"
)

// ServalSource is the generated_by tag applied to traffic imported from Serval.
// It surfaces in the WebUI's source column and lets MCP/UI filter Serval rows.
const ServalSource = "serval"

// ServalSync optionally mirrors a Serval browser's captured traffic into lorg.
//
// Serval (a WKWebView security browser) deliberately does not sit behind a
// proxy — a TLS-terminating MITM would break the native Safari fingerprint it
// exists to preserve — so it captures plaintext itself and persists it to a
// burp-mcp-compatible SQLite DB. This poller reads that DB read-only and feeds
// each new row through SaveRequestToBackend, which dual-writes to lorg's legacy
// tables (read by the MCP tools) AND the active project DB (read by the WebUI).
// The net effect: Serval traffic shows up in both surfaces from one ingest path.
//
// It is entirely optional. When dbPath is empty the sync never starts, and a
// missing/unreadable DB leaves it dormant without affecting the rest of lorg.
type ServalSync struct {
	backend  *Backend
	dbPath   string
	interval time.Duration
	db       *sql.DB
}

// NewServalSync builds a poller for the given Serval traffic.db path. An
// interval <= 0 defaults to one second, matching Serval's own drain cadence.
func NewServalSync(backend *Backend, dbPath string, interval time.Duration) *ServalSync {
	if interval <= 0 {
		interval = time.Second
	}
	return &ServalSync{backend: backend, dbPath: dbPath, interval: interval}
}

// Start runs the poll loop until the process exits; intended to run in its own
// goroutine. The watermark table lives in lorg's own DB, keyed by Serval DB
// path, so restarts resume rather than re-import and distinct Serval projects
// track independently.
func (s *ServalSync) Start() {
	if _, err := s.backend.DB.Exec(`CREATE TABLE IF NOT EXISTS _serval_sync (
		db_path         TEXT PRIMARY KEY,
		last_request_id INTEGER NOT NULL DEFAULT 0,
		updated         TEXT
	)`); err != nil {
		log.Printf("[ServalSync] cannot create watermark table, disabling: %v", err)
		return
	}

	log.Printf("[ServalSync] enabled for %s (poll every %s)", s.dbPath, s.interval)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for range ticker.C {
		if err := s.poll(); err != nil {
			log.Printf("[ServalSync] poll error: %v", err)
		}
	}
}

// ensureOpen lazily opens the Serval DB read-only. A not-yet-created DB is not
// an error — the poller stays dormant and retries on the next tick.
func (s *ServalSync) ensureOpen() error {
	if s.db != nil {
		return nil
	}
	if _, err := os.Stat(s.dbPath); err != nil {
		return nil // dormant until the DB appears
	}
	// modernc.org/sqlite honors the standard mode=ro URI param and applies
	// each _pragma on connection init. query_only is belt-and-suspenders so
	// we can never write Serval's DB.
	dsn := "file:" + s.dbPath + "?mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(true)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open serval db: %w", err)
	}
	db.SetMaxOpenConns(2)
	s.db = db
	return nil
}

// poll imports rows above the watermark, in request_id order, advancing the
// watermark only past rows that were successfully ingested.
func (s *ServalSync) poll() error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if s.db == nil {
		return nil // dormant
	}

	last, err := s.watermark()
	if err != nil {
		return fmt.Errorf("read watermark: %w", err)
	}

	rows, err := s.db.Query(`
		SELECT t.request_id, t.method, t.path, t.query, t.status_code, t.url,
		       m.request_headers, m.request_body, m.response_headers, m.response_body
		FROM http_traffic t
		LEFT JOIN http_messages m ON m.request_id = t.request_id
		WHERE t.request_id > ?
		ORDER BY t.request_id ASC
		LIMIT 200`, last)
	if err != nil {
		return fmt.Errorf("query serval traffic: %w", err)
	}
	defer rows.Close()

	maxID := last
	imported := 0
	for rows.Next() {
		var (
			id                      int64
			method, urlStr          string
			path, query             sql.NullString
			status                  sql.NullInt64
			reqHeaders, respHeaders sql.NullString
			reqBody, respBody       []byte
		)
		if err := rows.Scan(&id, &method, &path, &query, &status, &urlStr,
			&reqHeaders, &reqBody, &respHeaders, &respBody); err != nil {
			log.Printf("[ServalSync] scan request_id row: %v", err)
			continue
		}

		// Skip browser-internal pseudo-URLs (blob:, data:, about:,
		// javascript:). These are not network requests — a proxy never sees
		// them — and they only clutter the monitor with bogus hosts. Advance
		// the watermark past them so they aren't re-examined every tick.
		if isNonHTTPURL(urlStr) {
			if id > maxID {
				maxID = id
			}
			continue
		}

		body := types.AddRequestBodyType{
			// lorg derives the stored host from Url, expecting an origin
			// (scheme://host[:port]) — the request-line path/query come from
			// the raw request below. Passing the full URL here would leak the
			// path into the host column.
			Url:         servalOrigin(urlStr),
			Request:     buildServalRawRequest(method, path.String, query.String, reqHeaders.String, reqBody),
			Response:    buildServalRawResponse(int(status.Int64), respHeaders.String, respBody),
			GeneratedBy: ServalSource,
			Note:        "Imported from Serval",
		}
		if _, err := s.backend.SaveRequestToBackend(body); err != nil {
			// Stop the batch so the watermark never skips a row that failed;
			// it will be retried on the next tick.
			log.Printf("[ServalSync] save request_id=%d failed, will retry: %v", id, err)
			break
		}
		if id > maxID {
			maxID = id
		}
		imported++
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if maxID > last {
		if err := s.setWatermark(maxID); err != nil {
			return fmt.Errorf("update watermark: %w", err)
		}
		log.Printf("[ServalSync] imported %d row(s), watermark=%d", imported, maxID)
	}
	return nil
}

func (s *ServalSync) watermark() (int64, error) {
	var last int64
	err := s.backend.DB.QueryRow(
		`SELECT last_request_id FROM _serval_sync WHERE db_path = ?`, s.dbPath,
	).Scan(&last)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return last, err
}

func (s *ServalSync) setWatermark(id int64) error {
	now := time.Now().UTC().Format("2006-01-02 15:04:05.000Z")
	_, err := s.backend.DB.Exec(`
		INSERT INTO _serval_sync (db_path, last_request_id, updated) VALUES (?, ?, ?)
		ON CONFLICT(db_path) DO UPDATE SET
			last_request_id = excluded.last_request_id,
			updated = excluded.updated`,
		s.dbPath, id, now)
	return err
}

// servalOrigin reduces a full URL to its origin (scheme://host[:port]), the
// form lorg expects for host derivation. Falls back to the input unchanged if
// it can't be parsed.
func servalOrigin(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	return u.Scheme + "://" + u.Host
}

// isNonHTTPURL reports whether a URL is a browser-internal pseudo-URL that
// never crosses the network and so should not be mirrored as traffic.
func isNonHTTPURL(url string) bool {
	switch {
	case strings.HasPrefix(url, "blob:"),
		strings.HasPrefix(url, "data:"),
		strings.HasPrefix(url, "about:"),
		strings.HasPrefix(url, "javascript:"):
		return true
	}
	return false
}

// buildServalRawRequest reassembles a raw HTTP request from Serval's stored parts.
// Serval stores request_headers as "Name: Value" lines joined by CRLF (Host
// first, browser headers synthesized), with no trailing CRLF.
func buildServalRawRequest(method, path, query, headers string, body []byte) string {
	target := path
	if target == "" {
		target = "/"
	}
	if query != "" {
		target += "?" + query
	}
	var b strings.Builder
	b.WriteString(method)
	b.WriteByte(' ')
	b.WriteString(target)
	b.WriteString(" HTTP/1.1\r\n")
	if headers != "" {
		b.WriteString(headers)
		b.WriteString("\r\n")
	}
	b.WriteString("\r\n")
	if len(body) > 0 {
		b.Write(body)
	}
	return b.String()
}

// buildServalRawResponse reassembles a raw HTTP response. Serval does not store the
// reason phrase, so it is derived from the status code. A non-positive status
// means no response was captured, so the response is left empty (callers treat
// an empty Response as request-only).
func buildServalRawResponse(status int, headers string, body []byte) string {
	if status <= 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("HTTP/1.1 ")
	b.WriteString(strconv.Itoa(status))
	b.WriteByte(' ')
	b.WriteString(http.StatusText(status))
	b.WriteString("\r\n")
	if headers != "" {
		b.WriteString(headers)
		b.WriteString("\r\n")
	}
	b.WriteString("\r\n")
	if len(body) > 0 {
		b.Write(body)
	}
	return b.String()
}
