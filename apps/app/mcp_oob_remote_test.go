package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// TestOOBRemoteIntegration exercises the lorg ↔ remote lorg-oob flow.
// Requires a running lorg-oob server. Configure via environment variables:
//
//	OOB_API_URL=https://your-vps:8443
//	OOB_API_TOKEN=your-bearer-token
//	OOB_TRAP_URL=http://your-vps:9999
//
// Run: OOB_API_URL=https://... OOB_API_TOKEN=... OOB_TRAP_URL=http://... \
//
//	go test -run TestOOBRemoteIntegration -v ./apps/app/ -count=1
func TestOOBRemoteIntegration(t *testing.T) {
	remoteAPI := os.Getenv("OOB_API_URL")
	remoteToken := os.Getenv("OOB_API_TOKEN")
	httpTrap := os.Getenv("OOB_TRAP_URL")

	if remoteAPI == "" || remoteToken == "" || httpTrap == "" {
		t.Skip("skipping: set OOB_API_URL, OOB_API_TOKEN, OOB_TRAP_URL to run remote OOB integration tests")
	}

	backend := &Backend{}

	// Helper to extract JSON text from MCP result
	extractJSON := func(t *testing.T, result *mcp.CallToolResult) map[string]any {
		t.Helper()
		if result.IsError {
			t.Fatalf("MCP result is error: %v", result.Content)
		}
		for _, c := range result.Content {
			if tc, ok := c.(mcp.TextContent); ok {
				var data map[string]any
				if err := json.Unmarshal([]byte(tc.Text), &data); err == nil {
					return data
				}
			}
		}
		t.Fatal("no JSON text content in MCP result")
		return nil
	}

	// --- 0. Verify remote is reachable ---
	t.Run("health", func(t *testing.T) {
		resp, err := oobHTTPClient.Get(remoteAPI + "/api/health")
		if err != nil {
			t.Fatalf("cannot reach remote OOB server: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("health returned %d", resp.StatusCode)
		}
		var health map[string]any
		json.NewDecoder(resp.Body).Decode(&health)
		if health["status"] != "ok" {
			t.Fatalf("unexpected health: %v", health)
		}
		t.Logf("remote healthy: %v", health)
	})

	// --- 1. Connect remote ---
	t.Run("connectRemote", func(t *testing.T) {
		// Reset singleton
		oob.mu.Lock()
		oob.isRemote = false
		oob.remoteURL = ""
		oob.remoteToken = ""
		oob.running = false
		oob.mu.Unlock()

		result, err := backend.oobConnectRemoteHandler(OOBArgs{
			RemoteURL:   remoteAPI,
			RemoteToken: remoteToken,
		})
		if err != nil {
			t.Fatalf("connectRemote: %v", err)
		}
		data := extractJSON(t, result)
		if data["success"] != true {
			t.Fatalf("connectRemote failed: %v", data)
		}

		oob.mu.Lock()
		defer oob.mu.Unlock()
		if !oob.isRemote || !oob.running {
			t.Fatal("oob not in remote mode")
		}
		t.Logf("connected: mode=%v url=%v", data["mode"], data["url"])
	})

	// --- 2. Clear prior interactions ---
	t.Run("clear", func(t *testing.T) {
		result, err := backend.oobClearHandler()
		if err != nil {
			t.Fatal(err)
		}
		data := extractJSON(t, result)
		t.Logf("cleared %v interactions", data["cleared"])
	})

	// --- 3. Generate payload ---
	var generatedToken string
	t.Run("generatePayload", func(t *testing.T) {
		result, err := backend.oobGeneratePayloadHandler(OOBArgs{Host: "66.29.149.83"})
		if err != nil {
			t.Fatal(err)
		}
		data := extractJSON(t, result)
		tok, ok := data["token"].(string)
		if !ok || tok == "" {
			t.Fatalf("no token in response: %v", data)
		}
		generatedToken = tok
		t.Logf("token=%s http_url=%v", tok, data["http_url"])
		if payloads, ok := data["payloads"].(map[string]any); ok {
			t.Logf("payload types: %d", len(payloads))
			for k := range payloads {
				t.Logf("  - %s", k)
			}
		}
	})

	// --- 4. Fire HTTP callbacks ---
	t.Run("fireCallbacks", func(t *testing.T) {
		if generatedToken == "" {
			t.Skip("no token")
		}
		urls := []string{
			fmt.Sprintf("%s/%s", httpTrap, generatedToken),
			fmt.Sprintf("%s/%s/ssrf?x=1", httpTrap, generatedToken),
		}
		for _, u := range urls {
			resp, err := http.Get(u)
			if err != nil {
				t.Fatalf("callback to %s failed: %v", u, err)
			}
			resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Fatalf("callback %s returned %d", u, resp.StatusCode)
			}
			t.Logf("callback OK: %s", u)
		}
		time.Sleep(500 * time.Millisecond)
	})

	// --- 5. Poll interactions ---
	t.Run("pollInteractions", func(t *testing.T) {
		if generatedToken == "" {
			t.Skip("no token")
		}
		result, err := backend.oobPollHandler(OOBArgs{Token: generatedToken})
		if err != nil {
			t.Fatal(err)
		}
		data := extractJSON(t, result)
		count, _ := data["count"].(float64)
		if count < 2 {
			t.Fatalf("expected >= 2 interactions, got %v", count)
		}
		t.Logf("polled %v interactions (mode=%v)", count, data["mode"])

		if interactions, ok := data["interactions"].([]any); ok {
			for i, inter := range interactions {
				if m, ok := inter.(map[string]any); ok {
					t.Logf("  [%d] token=%v type=%v method=%v path=%v src=%v",
						i, m["token"], m["type"], m["method"], m["path"], m["source_ip"])
				}
			}
		}
	})

	// --- 6. Poll all (no token filter) ---
	t.Run("pollAll", func(t *testing.T) {
		result, err := backend.oobPollHandler(OOBArgs{})
		if err != nil {
			t.Fatal(err)
		}
		data := extractJSON(t, result)
		count, _ := data["count"].(float64)
		t.Logf("all interactions: %v", count)
	})

	// --- 7. Clear and verify empty ---
	t.Run("clearAndVerify", func(t *testing.T) {
		result, err := backend.oobClearHandler()
		if err != nil {
			t.Fatal(err)
		}
		data := extractJSON(t, result)
		t.Logf("cleared %v", data["cleared"])

		result, err = backend.oobPollHandler(OOBArgs{})
		if err != nil {
			t.Fatal(err)
		}
		data = extractJSON(t, result)
		count, _ := data["count"].(float64)
		if count != 0 {
			t.Fatalf("expected 0 after clear, got %v", count)
		}
		t.Log("post-clear: 0 interactions")
	})

	// --- 8. Disconnect ---
	t.Run("disconnect", func(t *testing.T) {
		result, err := backend.oobStopHandler()
		if err != nil {
			t.Fatal(err)
		}
		data := extractJSON(t, result)
		if data["success"] != true {
			t.Fatalf("stop failed: %v", data)
		}

		oob.mu.Lock()
		defer oob.mu.Unlock()
		if oob.isRemote || oob.running {
			t.Fatal("oob should be stopped")
		}
		t.Log("disconnected OK")
	})
}
