package rawhttp

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDumpRequest_IncludesHost(t *testing.T) {
	// Case 1: Host header in Header map
	req1, _ := http.NewRequest("GET", "http://example.com/path", nil)
	req1.Header.Set("Host", "example.com")
	req1.Host = "example.com"
	dump1 := DumpRequest(req1, true)
	if !strings.Contains(dump1, "Host: example.com") {
		t.Errorf("DumpRequest should contain Host header from Header map. Got:\n%s", dump1)
	}

	// Case 2: Host header NOT in Header map, but in req.Host
	req2, _ := http.NewRequest("GET", "http://example.com/path", nil)
	req2.Header = make(http.Header) // Clear headers
	req2.Host = "example.com"
	dump2 := DumpRequest(req2, true)
	if !strings.Contains(dump2, "Host: example.com") {
		t.Errorf("DumpRequest should contain Host header from req.Host. Got:\n%s", dump2)
	}

	// Case 3: Verify no duplicate Host headers
	req3, _ := http.NewRequest("GET", "http://example.com/path", nil)
	req3.Header.Set("Host", "example.com")
	req3.Host = "example.com"
	dump3 := DumpRequest(req3, true)
	count := strings.Count(dump3, "Host: example.com")
	if count != 1 {
		t.Errorf("DumpRequest should have exactly one Host header. Got %d. Dump:\n%s", count, dump3)
	}
}

func TestDumpRequest_RestoresBody(t *testing.T) {
	bodyText := "hello world"
	req, _ := http.NewRequest("POST", "http://example.com/path", strings.NewReader(bodyText))

	_ = DumpRequest(req, true)

	// Verify body is restored
	restoredBody, _ := io.ReadAll(req.Body)
	if string(restoredBody) != bodyText {
		t.Errorf("DumpRequest failed to restore body. Expected %q, got %q", bodyText, string(restoredBody))
	}
}
