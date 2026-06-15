package app

import (
	"fmt"
	"testing"

	"github.com/campbellcharlie/lorg/internal/types"
)

// TestUnionTrafficRows is the ADR-004 E2 proof: the cross-project read layer
// merges http_traffic from every project DB, tags each row with its project,
// orders globally by global_seq, supports a single-project filter, and honors a
// global limit.
func TestUnionTrafficRows(t *testing.T) {
	dir := t.TempDir()
	p := &ProjectDB{}
	if err := p.Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := p.SetProject("Base", dir); err != nil {
		t.Fatalf("SetProject: %v", err)
	}

	// Interleave rows across two projects so global ordering is non-trivial.
	mk := func(project, path string) {
		ud := types.UserData{
			Host: "h.com", Port: "443", IsHTTPS: true, GeneratedBy: "proxy/http",
			ReqJson:  types.RequestData{Method: "GET", Path: path},
			RespJson: types.ResponseData{Status: 200, Mime: "text/html"},
			Project:  project,
		}
		if err := p.LogTraffic(ud, "GET "+path+" HTTP/1.1\r\nHost: h.com\r\n\r\n", "HTTP/1.1 200 OK\r\n\r\nok"); err != nil {
			t.Fatalf("LogTraffic: %v", err)
		}
	}
	for i := 0; i < 6; i++ {
		if i%2 == 0 {
			mk("Red", fmt.Sprintf("/red%d", i))
		} else {
			mk("Blue", fmt.Sprintf("/blue%d", i))
		}
	}

	// Union across ALL projects.
	all, err := p.unionTrafficRows("", "", nil, 0)
	if err != nil {
		t.Fatalf("union all: %v", err)
	}
	if len(all) != 6 {
		t.Fatalf("union returned %d rows, want 6", len(all))
	}
	// Globally ordered by global_seq DESC.
	for i := 1; i < len(all); i++ {
		if all[i-1].GlobalSeq < all[i].GlobalSeq {
			t.Errorf("rows not globally ordered at %d", i)
		}
	}
	// Project tags present, both projects represented.
	seen := map[string]int{}
	for _, r := range all {
		seen[r.Project]++
	}
	if seen["Red"] != 3 || seen["Blue"] != 3 {
		t.Errorf("project tagging wrong: %v", seen)
	}

	// Single-project filter.
	red, err := p.unionTrafficRows("Red", "", nil, 0)
	if err != nil {
		t.Fatalf("union Red: %v", err)
	}
	if len(red) != 3 {
		t.Errorf("Red filter returned %d, want 3", len(red))
	}
	for _, r := range red {
		if r.Project != "Red" {
			t.Errorf("Red filter leaked project %q", r.Project)
		}
	}

	// WHERE pushdown + global limit.
	gets, err := p.unionTrafficRows("", "method = ?", []any{"GET"}, 2)
	if err != nil {
		t.Fatalf("union where+limit: %v", err)
	}
	if len(gets) != 2 {
		t.Errorf("limit not honored: got %d, want 2", len(gets))
	}
}

// TestCompositeIDByteRoundTrip is the ADR-004 E3 proof: a union row's composite
// id resolves back to the exact raw request/response via http_messages, so
// getRequestResponseFromID / mirror work without _data.
func TestCompositeIDByteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := &ProjectDB{}
	if err := p.Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := p.SetProject("Eng", dir); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	rawReq := "POST /login HTTP/1.1\r\nHost: x.com\r\nContent-Length: 9\r\n\r\nuser=root"
	rawResp := "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\n\r\n<html>ok</html>"
	ud := types.UserData{
		Host: "x.com", Port: "443", IsHTTPS: true, GeneratedBy: "ai/mcp/http",
		ReqJson:  types.RequestData{Method: "POST", Path: "/login"},
		RespJson: types.ResponseData{Status: 200, Mime: "text/html"},
	}
	if err := p.LogTraffic(ud, rawReq, rawResp); err != nil {
		t.Fatalf("LogTraffic: %v", err)
	}

	rows, err := p.unionTrafficRows("Eng", "", nil, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("union: err=%v rows=%d", err, len(rows))
	}
	rid := rows[0].RequestID

	// Composite id round-trips through parseRowID.
	id := makeRowID("Eng", rid)
	proj, n, ok := parseRowID(id)
	if !ok || proj != "Eng" || n != rid {
		t.Fatalf("parseRowID(%q) = %q,%d,%v", id, proj, n, ok)
	}
	// Legacy / malformed ids are not mistaken for composite ids.
	if _, _, ok := parseRowID("abcdef123"); ok {
		t.Errorf("legacy id parsed as composite")
	}
	if _, _, ok := parseRowID("Eng:notanumber"); ok {
		t.Errorf("non-numeric suffix parsed as composite")
	}

	// Bytes reconstruct EXACTLY.
	gotReq, gotResp, err := p.getTrafficBytes("Eng", rid)
	if err != nil {
		t.Fatalf("getTrafficBytes: %v", err)
	}
	if gotReq != rawReq {
		t.Errorf("request mismatch:\n got %q\nwant %q", gotReq, rawReq)
	}
	if gotResp != rawResp {
		t.Errorf("response mismatch:\n got %q\nwant %q", gotResp, rawResp)
	}
}
