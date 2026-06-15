package app

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Cross-project UNION read layer (ADR-004 E2).
//
// _data is one global lorgdb table holding every project's rows; http_traffic is
// split across per-project .db files. To retire _data, every reader that queried
// the global table must instead query http_traffic in EACH project DB and merge.
// unionTrafficRows is that primitive: it runs a parameterized WHERE against
// http_traffic in every project DB under the projects dir (read-only), tags each
// row with its project, merges, sorts by global_seq DESC, and trims to a global
// limit. global_seq (UnixNano, ADR-004 E1) gives a stable cross-project ordering.

// TrafficRow is the unified row shape returned by the union layer, covering the
// fields _data readers use.
type TrafficRow struct {
	Project     string
	RequestID   int64
	GlobalSeq   int64
	Timestamp   string
	Tool        string
	GeneratedBy string
	Method      string
	Host        string
	Path        string
	Query       string
	URL         string
	Status      int
	RespLength  int
	Mime        string
	Fingerprint string
	RequestHash string
}

const unionTrafficCols = `request_id, global_seq, timestamp, tool, generated_by,
	method, host, path, query, url, status_code, response_length, mime_type,
	fingerprint, request_hash`

// listProjectDBFiles returns the project name -> .db path map for every project
// DB under dbDir (TemporaryProject excluded — it's the unconfigured default).
func listProjectDBFiles(dbDir string) (map[string]string, error) {
	out := make(map[string]string)
	if dbDir == "" {
		return out, nil
	}
	entries, err := os.ReadDir(dbDir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".db") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".db")
		if name == "TemporaryProject" {
			continue
		}
		out[name] = filepath.Join(dbDir, e.Name())
	}
	return out, nil
}

// scanTrafficRows runs the union SELECT against one project DB and tags rows with
// the project name.
func scanTrafficRows(dbFile, project, where string, args []any, limit int) ([]TrafficRow, error) {
	db, err := sql.Open("sqlite", "file:"+dbFile+"?mode=ro&_query_only=on&_busy_timeout=3000")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	q := "SELECT " + unionTrafficCols + " FROM http_traffic"
	if strings.TrimSpace(where) != "" {
		q += " WHERE " + where
	}
	q += " ORDER BY global_seq DESC"
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := db.Query(q, args...)
	if err != nil {
		// A project DB missing the v6 columns (or otherwise unreadable) is
		// skipped rather than failing the whole union.
		return nil, nil
	}
	defer rows.Close()

	var out []TrafficRow
	for rows.Next() {
		var r TrafficRow
		r.Project = project
		if err := rows.Scan(&r.RequestID, &r.GlobalSeq, &r.Timestamp, &r.Tool, &r.GeneratedBy,
			&r.Method, &r.Host, &r.Path, &r.Query, &r.URL, &r.Status, &r.RespLength,
			&r.Mime, &r.Fingerprint, &r.RequestHash); err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// unionTrafficRows queries http_traffic across all project DBs and returns the
// merged, globally-ordered result. `where`/`args` are applied per-DB (must
// reference http_traffic columns). `projectFilter`, if non-empty, restricts to a
// single project (the common case — then it's just that one DB). limit<=0 means
// no limit.
func (p *ProjectDB) unionTrafficRows(projectFilter, where string, args []any, limit int) ([]TrafficRow, error) {
	p.mu.RLock()
	dbDir := p.dbDir
	p.mu.RUnlock()

	files, err := listProjectDBFiles(dbDir)
	if err != nil {
		return nil, fmt.Errorf("unionTrafficRows: list project DBs: %w", err)
	}
	if projectFilter != "" {
		pf := sanitizeProjectName(projectFilter)
		if path, ok := files[pf]; ok {
			files = map[string]string{pf: path}
		} else {
			return nil, nil // unknown project -> empty, not an error
		}
	}

	var all []TrafficRow
	for name, path := range files {
		rows, err := scanTrafficRows(path, name, where, args, limit)
		if err != nil {
			return all, fmt.Errorf("unionTrafficRows: %s: %w", name, err)
		}
		all = append(all, rows...)
	}

	sort.Slice(all, func(i, j int) bool { return all[i].GlobalSeq > all[j].GlobalSeq })
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// Composite row identity (ADR-004). _data used a single global string id;
// http_traffic rows are identified per-project by (project, request_id). The
// composite id "project:request_id" lets union-sourced readers hand clients an id
// that getRequestResponseFromID / mirror can resolve back to the right project DB.

// makeRowID builds the composite id for a union row.
func makeRowID(project string, requestID int64) string {
	return fmt.Sprintf("%s:%d", project, requestID)
}

// parseRowID splits a composite id back into (project, request_id). ok=false when
// the string isn't a composite id (e.g. a legacy _data id), so callers can fall
// back. Splits on the LAST colon so project names with colons still parse.
func parseRowID(id string) (project string, requestID int64, ok bool) {
	i := strings.LastIndex(id, ":")
	if i < 0 {
		return "", 0, false
	}
	var n int64
	if _, err := fmt.Sscanf(id[i+1:], "%d", &n); err != nil {
		return "", 0, false
	}
	return id[:i], n, true
}

// getTrafficBytes reconstructs the raw request/response for one row from a
// project DB's http_messages (ADR-004 E7 foundation). Reconstruction mirrors
// splitHTTPRaw's split point so the bytes round-trip.
func (p *ProjectDB) getTrafficBytes(project string, requestID int64) (rawReq, rawResp string, err error) {
	p.mu.RLock()
	dbDir := p.dbDir
	p.mu.RUnlock()

	files, err := listProjectDBFiles(dbDir)
	if err != nil {
		return "", "", err
	}
	path, ok := files[sanitizeProjectName(project)]
	if !ok {
		return "", "", fmt.Errorf("unknown project %q", project)
	}

	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_query_only=on&_busy_timeout=3000")
	if err != nil {
		return "", "", err
	}
	defer db.Close()

	var reqH, reqB, respH, respB string
	row := db.QueryRow(`SELECT request_headers, request_body, response_headers, response_body
		FROM http_messages WHERE request_id = ?`, requestID)
	if err := row.Scan(&reqH, &reqB, &respH, &respB); err != nil {
		return "", "", err
	}
	return rejoinRaw(reqH, reqB), rejoinRaw(respH, respB), nil
}

// rejoinRaw reconstructs a raw HTTP message from its split header/body halves.
func rejoinRaw(headers, body string) string {
	if headers == "" && body == "" {
		return ""
	}
	return headers + "\r\n\r\n" + body
}
