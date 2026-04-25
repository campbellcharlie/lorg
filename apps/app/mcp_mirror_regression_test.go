package app

import (
	"encoding/json"
	"strings"
	"testing"
)

// Regression: when the baseline request's header block had a trailing
// newline, split("\n") produced a trailing "" entry. Newly-added headers
// (Content-Type, Content-Length from a body mutation) landed AFTER that
// empty line, which the receiver then parsed as "end of headers → body
// starts here", so the new headers were transmitted as part of the body.
//
// Discovered live: mirroring a GET /json with method=POST + body={...}
// caused httpbin to echo back "Content-Type: application/json\r\n..."
// inside its `data` field instead of just the JSON.
func TestMirror_NoBlankLineBetweenOriginalAndAppendedHeaders(t *testing.T) {
	// Note the trailing \r\n\r\n separator + empty body, like a real
	// captured GET would store.
	base := "GET /json HTTP/1.1\r\nHost: httpbin.org\r\nAccept: */*\r\nConnection: close\r\n\r\n"
	out, _, err := applyMutations(base, MirrorArgs{
		Method: "POST",
		Path:   "/anything",
		Body:   json.RawMessage(`{"forced":"post"}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Find the FIRST \r\n\r\n — everything before is headers, after is body.
	sep := strings.Index(out, "\r\n\r\n")
	if sep < 0 {
		t.Fatalf("no header/body separator: %q", out)
	}
	headers := out[:sep]
	body := out[sep+4:]

	// Header block must contain BOTH Content-Type and Content-Length.
	if !strings.Contains(headers, "Content-Type: application/json") {
		t.Errorf("Content-Type should be in headers, not body. headers=%q", headers)
	}
	if !strings.Contains(headers, "Content-Length: 17") {
		t.Errorf("Content-Length should be in headers, not body. headers=%q", headers)
	}
	// Body must contain ONLY the JSON we asked for, no header leakage.
	if body != `{"forced":"post"}` {
		t.Errorf("body should be exactly the new JSON, got %q", body)
	}
}
