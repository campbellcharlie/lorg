package app

import (
	"bufio"
	"io"
	"net/http"
	"strings"
	"testing"
)

// The reconstructed bytes must parse as real HTTP — that is the load-bearing
// assumption behind feeding them to SaveRequestToBackend. Serval stores headers
// as "Name: Value" lines joined by CRLF with Host first and no trailing CRLF.
func TestBuildServalRawRequest_ParsesAsHTTP(t *testing.T) {
	headers := "Host: example.com\r\nUser-Agent: Serval\r\nContent-Type: application/json\r\nContent-Length: 9"
	raw := buildServalRawRequest("POST", "/api/login", "next=/home", headers, []byte(`{"u":"a"}`))

	req, err := http.ReadRequest(bufio.NewReader(strings.NewReader(raw)))
	if err != nil {
		t.Fatalf("reconstructed request did not parse: %v\n---\n%s", err, raw)
	}
	if req.Method != "POST" {
		t.Errorf("method = %q, want POST", req.Method)
	}
	if req.URL.RequestURI() != "/api/login?next=/home" {
		t.Errorf("request-uri = %q, want /api/login?next=/home", req.URL.RequestURI())
	}
	if req.Host != "example.com" {
		t.Errorf("host = %q, want example.com", req.Host)
	}
	body, _ := io.ReadAll(req.Body)
	if string(body) != `{"u":"a"}` {
		t.Errorf("body = %q, want the JSON payload", body)
	}
}

// An empty path must still produce a valid request line ("/"), and a request
// with no body / no query must round-trip.
func TestBuildServalRawRequest_Defaults(t *testing.T) {
	raw := buildServalRawRequest("GET", "", "", "Host: h.test", nil)
	req, err := http.ReadRequest(bufio.NewReader(strings.NewReader(raw)))
	if err != nil {
		t.Fatalf("did not parse: %v\n---\n%s", err, raw)
	}
	if req.URL.RequestURI() != "/" {
		t.Errorf("request-uri = %q, want /", req.URL.RequestURI())
	}
}

func TestBuildServalRawResponse_ParsesAsHTTP(t *testing.T) {
	headers := "Content-Type: text/html\r\nContent-Length: 5"
	raw := buildServalRawResponse(200, headers, []byte("hello"))

	resp, err := http.ReadResponse(bufio.NewReader(strings.NewReader(raw)), nil)
	if err != nil {
		t.Fatalf("reconstructed response did not parse: %v\n---\n%s", err, raw)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello" {
		t.Errorf("body = %q, want hello", body)
	}
}

func TestServalOrigin(t *testing.T) {
	cases := map[string]string{
		"https://www.reddit.com/r/programming/":                "https://www.reddit.com",
		"https://googleads.g.doubleclick.net/pagead/x?a=1&b=2": "https://googleads.g.doubleclick.net",
		"http://example.com:8080/path":                         "http://example.com:8080",
		"https://accounts.google.com/gsi/client":               "https://accounts.google.com",
	}
	for in, want := range cases {
		if got := servalOrigin(in); got != want {
			t.Errorf("servalOrigin(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsNonHTTPURL(t *testing.T) {
	skip := []string{
		"blob:https://www.reddit.com/8c695366-c232",
		"data:image/png;base64,iVBORw0KGg",
		"data:text/javascript,window.x=1",
		"about:blank",
		"javascript:void(0)",
	}
	keep := []string{
		"https://www.reddit.com/r/programming/",
		"http://example.com/data",
		"https://styles.redditmedia.com/blob/icon.png",
	}
	for _, u := range skip {
		if !isNonHTTPURL(u) {
			t.Errorf("isNonHTTPURL(%q) = false, want true", u)
		}
	}
	for _, u := range keep {
		if isNonHTTPURL(u) {
			t.Errorf("isNonHTTPURL(%q) = true, want false", u)
		}
	}
}

// A non-positive status means Serval captured no response; the builder must
// return empty so SaveRequestToBackend treats the row as request-only.
func TestBuildServalRawResponse_NoResponse(t *testing.T) {
	if got := buildServalRawResponse(0, "", nil); got != "" {
		t.Errorf("status 0 produced %q, want empty", got)
	}
}
