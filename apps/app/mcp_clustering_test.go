package app

import (
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// setupClusterTestDB stands up an in-memory SQLite DB with the _data schema
// shape we need for the cluster queries, plus a few rows with hand-set
// fingerprints. Returns a DB handle the caller can use directly with the
// query strings under test.
func setupClusterTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if _, err := db.Exec(`
		CREATE TABLE _data (
			id          TEXT PRIMARY KEY NOT NULL,
			"index"     REAL NOT NULL DEFAULT 0,
			host        TEXT NOT NULL DEFAULT '',
			has_resp    BOOLEAN NOT NULL DEFAULT FALSE,
			req_json    JSON DEFAULT NULL,
			resp_json   JSON DEFAULT NULL,
			fingerprint TEXT NOT NULL DEFAULT ''
		)
	`); err != nil {
		t.Fatalf("schema: %v", err)
	}

	rows := []struct {
		id, host, method, path, fp, mime string
		status                           int
		hasResp                          bool
	}{
		// 6 "modal" responses on /api/users — same fingerprint
		{"a1", "api.example.com", "GET", "/api/users", "s200-mjson-l3-haaaaaaaa", "application/json", 200, true},
		{"a2", "api.example.com", "GET", "/api/users", "s200-mjson-l3-haaaaaaaa", "application/json", 200, true},
		{"a3", "api.example.com", "GET", "/api/users", "s200-mjson-l3-haaaaaaaa", "application/json", 200, true},
		{"a4", "api.example.com", "GET", "/api/users", "s200-mjson-l3-haaaaaaaa", "application/json", 200, true},
		{"a5", "api.example.com", "GET", "/api/users", "s200-mjson-l3-haaaaaaaa", "application/json", 200, true},
		{"a6", "api.example.com", "GET", "/api/users", "s200-mjson-l3-haaaaaaaa", "application/json", 200, true},
		// 1 anomaly on the same endpoint — different fingerprint (e.g. error)
		{"b1", "api.example.com", "GET", "/api/users", "s500-mhtml-l5-hcccccccc", "text/html", 500, true},
		// 1 anomaly with elevated info — different shape (e.g. admin leak)
		{"b2", "api.example.com", "GET", "/api/users", "s200-mjson-l4-hbbbbbbbb", "application/json", 200, true},
		// Different endpoint, should NOT pollute /api/users analysis
		{"c1", "api.example.com", "GET", "/api/posts", "s200-mjson-l2-hdddddddd", "application/json", 200, true},
		// has_resp=false — must be excluded
		{"d1", "api.example.com", "GET", "/api/users", "s000-mnone-l0-h00000000", "", 0, false},
		// fingerprint='' — must be excluded
		{"e1", "api.example.com", "GET", "/api/users", "", "", 0, true},
	}

	for i, r := range rows {
		reqJSON := `{"method":"` + r.method + `","path":"` + r.path + `"}`
		respJSON := `{"status":` + itoa(r.status) + `,"mime":"` + r.mime + `"}`
		if _, err := db.Exec(
			`INSERT INTO _data (id, "index", host, has_resp, req_json, resp_json, fingerprint)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			r.id, float64(i), r.host, r.hasResp, reqJSON, respJSON, r.fp,
		); err != nil {
			t.Fatalf("insert %s: %v", r.id, err)
		}
	}

	return db
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// TestCluster_ModalAndAnomalyQueries runs the actual SQL strings used by the
// clustering handlers against a controlled fixture DB. It verifies:
//   - clusters are grouped correctly with the modal one ranked first
//   - anomaly detection picks up exactly the non-modal rows on an endpoint
//   - has_resp=false and fingerprint='' rows are filtered out
//   - a different endpoint does not pollute the result
func TestCluster_ModalAndAnomalyQueries(t *testing.T) {
	db := setupClusterTestDB(t)
	defer db.Close()

	where, whereArgs := buildClusterWhere("api.example.com", "GET", "/api/users", true)
	if !strings.Contains(where, "host LIKE") {
		t.Errorf("expected host clause, got %q", where)
	}

	// --- Cluster query --------------------------------------------------
	clusterQ := `
		SELECT
			fingerprint,
			COUNT(*),
			COALESCE(json_extract(resp_json,'$.status'), 0),
			COALESCE(json_extract(resp_json,'$.mime'),  '')
		FROM _data ` + where + `
		  AND fingerprint != ''
		  AND has_resp = TRUE
		GROUP BY fingerprint
		ORDER BY COUNT(*) DESC`

	rows, err := db.Query(clusterQ, whereArgs...)
	if err != nil {
		t.Fatalf("cluster query: %v", err)
	}
	defer rows.Close()

	type cluster struct {
		fp     string
		count  int
		status int
		mime   string
	}
	var clusters []cluster
	for rows.Next() {
		var c cluster
		if err := rows.Scan(&c.fp, &c.count, &c.status, &c.mime); err != nil {
			t.Fatalf("scan: %v", err)
		}
		clusters = append(clusters, c)
	}

	if len(clusters) != 3 {
		t.Fatalf("expected 3 clusters on /api/users, got %d (%v)", len(clusters), clusters)
	}
	if clusters[0].fp != "s200-mjson-l3-haaaaaaaa" || clusters[0].count != 6 {
		t.Errorf("modal cluster wrong: %+v", clusters[0])
	}

	// --- Anomaly query --------------------------------------------------
	modalFP := clusters[0].fp
	anomalyQ := `
		SELECT id, fingerprint
		FROM _data ` + where + `
		  AND fingerprint != ''
		  AND fingerprint != ?
		  AND has_resp = TRUE
		ORDER BY "index" DESC`

	anomArgs := append(append([]any{}, whereArgs...), modalFP)
	arows, err := db.Query(anomalyQ, anomArgs...)
	if err != nil {
		t.Fatalf("anomaly query: %v", err)
	}
	defer arows.Close()

	var ids []string
	for arows.Next() {
		var id, fp string
		if err := arows.Scan(&id, &fp); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
	}

	// Expect exactly b1 and b2 (the two non-modal rows on /api/users with has_resp).
	if len(ids) != 2 {
		t.Fatalf("expected 2 anomalies, got %d (%v)", len(ids), ids)
	}
	got := strings.Join(ids, ",")
	if !strings.Contains(got, "b1") || !strings.Contains(got, "b2") {
		t.Errorf("expected anomalies b1,b2 — got %s", got)
	}
}

func TestBuildClusterWhere_NoFilters(t *testing.T) {
	where, args := buildClusterWhere("", "", "", true)
	if where != "WHERE 1=1" {
		t.Errorf("expected default WHERE 1=1, got %q", where)
	}
	if len(args) != 0 {
		t.Errorf("expected no args, got %v", args)
	}
}

func TestBuildClusterWhere_AllFilters(t *testing.T) {
	where, args := buildClusterWhere("ex.com", "post", "/login", true)
	if !strings.Contains(where, "host LIKE") {
		t.Errorf("missing host: %q", where)
	}
	if !strings.Contains(where, "json_extract(req_json,'$.method')") {
		t.Errorf("missing method: %q", where)
	}
	if len(args) != 3 {
		t.Errorf("expected 3 args, got %d", len(args))
	}
	// Method should be uppercased.
	if args[1] != "POST" {
		t.Errorf("expected method uppercased, got %v", args[1])
	}
}
