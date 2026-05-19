package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/campbellcharlie/lorg/internal/config"
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

func TestRequireMCPAuth(t *testing.T) {
	const token = "secret-token-value"

	tests := []struct {
		name         string
		configToken  string
		authHeader   string // empty string => no header set
		remoteAddr   string
		trustNetwork bool
		wantNextRun  bool // true => handler should pass auth (return nil)
		wantStatus   int  // expected HTTP status when wantNextRun is false
	}{
		{"token configured, correct header", token, "Bearer " + token, "10.0.0.5", false, true, 0},
		{"token configured, missing header", token, "", "127.0.0.1", false, false, http.StatusUnauthorized},
		{"token configured, wrong prefix", token, "Basic " + token, "127.0.0.1", false, false, http.StatusUnauthorized},
		{"token configured, wrong value", token, "Bearer wrong-token", "127.0.0.1", false, false, http.StatusUnauthorized},
		{"no token, loopback IPv4", "", "", "127.0.0.1", false, true, 0},
		{"no token, loopback IPv6", "", "", "::1", false, true, 0},
		{"no token, LAN denied", "", "", "10.0.0.5", false, false, http.StatusUnauthorized},
		{"no token, LAN denied even with trustNetwork", "", "", "10.0.0.5", true, false, http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prev := trustNetwork
			trustNetwork = tt.trustNetwork
			defer func() { trustNetwork = prev }()

			backend := &Backend{Config: &config.Config{MCPToken: tt.configToken}}

			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			if strings.Contains(tt.remoteAddr, ":") {
				req.RemoteAddr = "[" + tt.remoteAddr + "]:12345"
			} else {
				req.RemoteAddr = tt.remoteAddr + ":12345"
			}
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			err := backend.requireMCPAuth(c)
			if err != nil {
				t.Fatalf("requireMCPAuth returned unexpected error: %v", err)
			}

			if tt.wantNextRun {
				if rec.Code != http.StatusOK { // default recorder status
					t.Errorf("expected pass-through (no response written), got status %d body=%q", rec.Code, rec.Body.String())
				}
			} else {
				if rec.Code != tt.wantStatus {
					t.Errorf("want status %d, got %d body=%q", tt.wantStatus, rec.Code, rec.Body.String())
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
