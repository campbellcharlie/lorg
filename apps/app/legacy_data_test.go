package app

import (
	"testing"

	"github.com/campbellcharlie/lorg/internal/types"
)

// TestLegacyDataWriteGate is the ADR-004 E9 proof: with the legacy _data write
// OFF, a captured request lands ONLY in the per-project http_traffic store (which
// the migrated agent readers serve) and writes nothing to lorgdb _data/_req/_resp.
func TestLegacyDataWriteGate(t *testing.T) {
	if raceEnabled {
		t.Skip("skips under -race: SaveRequestToBackend's pre-existing sitemap goroutine race")
	}
	backend := newProjectTagTestBackend(t)
	useGlobalProjectDB(t, t.TempDir(), "gateproj")

	SetLegacyDataWrites(false)
	t.Cleanup(func() { SetLegacyDataWrites(true) })

	body := types.AddRequestBodyType{
		Url:         "https://example.com",
		Request:     "GET /gated HTTP/1.1\r\nHost: example.com\r\n\r\n",
		Response:    "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\n\r\n<html></html>",
		GeneratedBy: "ai/mcp/http",
		Project:     "gateproj",
	}
	if _, err := backend.SaveRequestToBackend(body); err != nil {
		t.Fatalf("SaveRequestToBackend: %v", err)
	}

	// _data / _req / _resp must be EMPTY.
	for _, coll := range []string{"_data", "_req", "_resp"} {
		recs, _ := backend.DB.FindRecords(coll, "1=1")
		if len(recs) != 0 {
			t.Errorf("%s got %d rows with the legacy write off, want 0", coll, len(recs))
		}
	}

	// But the agent reader finds it via http_traffic.
	if got := queryUntil(t, backend, `req.method.eq:"GET"`, "gateproj", 1); got != 1 {
		t.Errorf("captured row not served from http_traffic with legacy write off: got %d", got)
	}
}
