package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestRequireLocalhost(t *testing.T) {
	tests := []struct {
		name         string
		remoteAddr   string
		trustNetwork bool
		wantErr      bool
		wantStatus   int
	}{
		{"loopback IPv4 allowed", "127.0.0.1", false, false, 0},
		{"loopback IPv6 allowed", "::1", false, false, 0},
		{"localhost string allowed", "localhost", false, false, 0},
		{"LAN address denied", "192.168.1.100", false, true, http.StatusForbidden},
		{"private 10.x denied", "10.0.0.1", false, true, http.StatusForbidden},
		{"public IP denied", "8.8.8.8", false, true, http.StatusForbidden},
		{"trustNetwork bypasses LAN", "192.168.1.100", true, false, 0},
		{"trustNetwork bypasses public", "8.8.8.8", true, false, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save and restore global trustNetwork state.
			prev := trustNetwork
			trustNetwork = tt.trustNetwork
			defer func() { trustNetwork = prev }()

			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			// IPv6 addresses require bracket notation for host:port.
			if strings.Contains(tt.remoteAddr, ":") {
				req.RemoteAddr = "[" + tt.remoteAddr + "]:12345"
			} else {
				req.RemoteAddr = tt.remoteAddr + ":12345"
			}
			// Echo's RealIP uses RemoteAddr when no proxy headers are present.
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			err := requireLocalhost(c)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (response status %d)", rec.Code)
				}
				he, ok := err.(*echo.HTTPError)
				if !ok {
					t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
				}
				if he.Code != tt.wantStatus {
					t.Errorf("want status %d, got %d", tt.wantStatus, he.Code)
				}
			} else {
				if err != nil {
					t.Fatalf("expected nil error, got %v", err)
				}
			}
		})
	}
}

func TestRequireAuth_DelegatesToLocalhost(t *testing.T) {
	// requireAuth is an alias — verify it delegates correctly.
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "8.8.8.8:1234"
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := requireAuth(c)
	if err == nil {
		t.Fatal("expected error from non-local address, got nil")
	}
}
