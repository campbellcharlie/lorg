package app

import (
	"strings"
	"testing"
)

// fixtureRows mirrors the old _data fixture but as union TrafficRows: 6 modal
// responses on /api/users, two anomalies, a different endpoint, and a blank
// fingerprint that must be excluded.
func fixtureRows() []TrafficRow {
	mk := func(project string, rid int64, method, path, fp string, status int, mime string) TrafficRow {
		return TrafficRow{Project: project, RequestID: rid, GlobalSeq: rid, Method: method, Path: path, Fingerprint: fp, Status: status, Mime: mime, Host: "api.example.com"}
	}
	return []TrafficRow{
		mk("A", 1, "GET", "/api/users", "s200-mjson-l3-haaaaaaaa", 200, "application/json"),
		mk("A", 2, "GET", "/api/users", "s200-mjson-l3-haaaaaaaa", 200, "application/json"),
		mk("B", 3, "GET", "/api/users", "s200-mjson-l3-haaaaaaaa", 200, "application/json"),
		mk("A", 4, "GET", "/api/users", "s200-mjson-l3-haaaaaaaa", 200, "application/json"),
		mk("B", 5, "GET", "/api/users", "s200-mjson-l3-haaaaaaaa", 200, "application/json"),
		mk("A", 6, "GET", "/api/users", "s200-mjson-l3-haaaaaaaa", 200, "application/json"),
		mk("A", 7, "GET", "/api/users", "s500-mhtml-l5-hcccccccc", 500, "text/html"),       // anomaly
		mk("B", 8, "GET", "/api/users", "s200-mjson-l4-hbbbbbbbb", 200, "application/json"), // anomaly
		mk("A", 9, "GET", "/api/users", "", 0, ""),                                          // blank fp — excluded
	}
}

// TestClusterByFingerprint verifies the modal cluster ranks first, blank
// fingerprints are excluded, the length bucket parses, and sample ids are the
// cross-project composite form (ADR-004 E5).
func TestClusterByFingerprint(t *testing.T) {
	clusters, total := clusterByFingerprint(fixtureRows(), 50)
	if len(clusters) != 3 {
		t.Fatalf("expected 3 clusters, got %d (%+v)", len(clusters), clusters)
	}
	if total != 8 {
		t.Errorf("expected 8 grouped rows (blank excluded), got %d", total)
	}
	if clusters[0].Fingerprint != "s200-mjson-l3-haaaaaaaa" || clusters[0].Count != 6 {
		t.Errorf("modal cluster wrong: %+v", clusters[0])
	}
	if clusters[0].LengthBkt != 3 {
		t.Errorf("length bucket parse: got %d want 3", clusters[0].LengthBkt)
	}
	// Sample ids are composite project:request_id.
	for _, id := range clusters[0].SampleIDs {
		if _, _, ok := parseRowID(id); !ok {
			t.Errorf("sample id %q is not a composite id", id)
		}
	}
}

// TestAnomaliesFromRows verifies the two non-modal rows are surfaced and the
// blank fingerprint is ignored.
func TestAnomaliesFromRows(t *testing.T) {
	modalFP, modalCount, anomalies := anomaliesFromRows(fixtureRows(), 25)
	if modalFP != "s200-mjson-l3-haaaaaaaa" || modalCount != 6 {
		t.Fatalf("modal wrong: %q count=%d", modalFP, modalCount)
	}
	if len(anomalies) != 2 {
		t.Fatalf("expected 2 anomalies, got %d (%+v)", len(anomalies), anomalies)
	}
	fps := anomalies[0].Fingerprint + "," + anomalies[1].Fingerprint
	if !strings.Contains(fps, "hcccccccc") || !strings.Contains(fps, "hbbbbbbbb") {
		t.Errorf("expected the two anomaly fingerprints, got %s", fps)
	}
}

func TestClusterWhereHT(t *testing.T) {
	where, args := clusterWhereHT("ex.com", "post", "/login")
	if !strings.Contains(where, "host LIKE") || !strings.Contains(where, "method = ?") || !strings.Contains(where, "path LIKE") {
		t.Errorf("missing clause: %q", where)
	}
	if !strings.Contains(where, "fingerprint != ''") {
		t.Errorf("fingerprint guard missing: %q", where)
	}
	if len(args) != 3 || args[1] != "POST" {
		t.Errorf("args wrong (method should be uppercased): %v", args)
	}

	// No filters still guards fingerprint.
	w2, a2 := clusterWhereHT("", "", "")
	if w2 != "fingerprint != ''" || len(a2) != 0 {
		t.Errorf("empty-filter where = %q args=%v", w2, a2)
	}
}
