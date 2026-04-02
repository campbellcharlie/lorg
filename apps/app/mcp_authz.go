package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// Authorization testing state
// ---------------------------------------------------------------------------

type authzConfig struct {
	highPrivSession string
	lowPrivSession  string
	unauthenticated bool // also test with no cookies
}

// authzCfg holds the current authorization testing configuration.
// Package-level because action-dispatch handlers access it without Backend reference.
// Protected by authzCfgMu for concurrent access.
var (
	authzCfg   authzConfig
	authzCfgMu sync.Mutex
)

// ---------------------------------------------------------------------------
// Input schemas
// ---------------------------------------------------------------------------

type AuthzTestArgs struct {
	Action          string `json:"action" jsonschema:"required,enum=configure,run,results" jsonschema_description:"configure: set session names; run: replay traffic with swapped sessions; results: show findings"`
	HighPrivSession string `json:"highPrivSession,omitempty" jsonschema_description:"Name of the high-privilege session (original requests)"`
	LowPrivSession  string `json:"lowPrivSession,omitempty" jsonschema_description:"Name of the low-privilege session to swap in"`
	Unauthenticated bool   `json:"unauthenticated,omitempty" jsonschema_description:"Also test with no cookies/auth headers"`
	Host            string `json:"host,omitempty" jsonschema_description:"Filter traffic by host for run action"`
	Limit           int    `json:"limit,omitempty" jsonschema_description:"Max requests to test (default 50)"`
}

// ---------------------------------------------------------------------------
// Results storage (in-memory per session)
// ---------------------------------------------------------------------------

type authzResult struct {
	RequestID      string  `json:"request_id"`
	Method         string  `json:"method"`
	Path           string  `json:"path"`
	Host           string  `json:"host"`
	OriginalStatus int     `json:"original_status"`
	LowPrivStatus  int     `json:"low_priv_status,omitempty"`
	UnauthStatus   int     `json:"unauth_status,omitempty"`
	BodySimilarity float64 `json:"body_similarity"`
	AccessControl  string  `json:"access_control"` // "enforced", "bypassed", "partial"
	Detail         string  `json:"detail,omitempty"`
}

// authzResults stores authorization test findings for the current session.
// Package-level because action-dispatch handlers access it without Backend reference.
// Protected by authzResultsMu for concurrent access.
var (
	authzResults   []authzResult
	authzResultsMu sync.Mutex
)

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func (backend *Backend) authzTestHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args AuthzTestArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "configure":
		return backend.authzConfigureHandler(args)
	case "run":
		return backend.authzRunHandler(args)
	case "results":
		return backend.authzResultsHandler(args)
	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: configure, run, results"), nil
	}
}

func (backend *Backend) authzConfigureHandler(args AuthzTestArgs) (*mcp.CallToolResult, error) {
	if args.HighPrivSession == "" || args.LowPrivSession == "" {
		return mcp.NewToolResultError("highPrivSession and lowPrivSession are required"), nil
	}

	// Verify sessions exist
	if _, err := backend.resolveSession(args.HighPrivSession); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("high-priv session %q not found: %v", args.HighPrivSession, err)), nil
	}
	if _, err := backend.resolveSession(args.LowPrivSession); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("low-priv session %q not found: %v", args.LowPrivSession, err)), nil
	}

	authzCfgMu.Lock()
	authzCfg = authzConfig{
		highPrivSession: args.HighPrivSession,
		lowPrivSession:  args.LowPrivSession,
		unauthenticated: args.Unauthenticated,
	}
	authzCfgMu.Unlock()

	return mcpJSONResult(map[string]any{
		"success":         true,
		"highPrivSession": args.HighPrivSession,
		"lowPrivSession":  args.LowPrivSession,
		"unauthenticated": args.Unauthenticated,
	})
}

func (backend *Backend) authzRunHandler(args AuthzTestArgs) (*mcp.CallToolResult, error) {
	authzCfgMu.Lock()
	cfg := authzCfg
	authzCfgMu.Unlock()

	if cfg.highPrivSession == "" || cfg.lowPrivSession == "" {
		return mcp.NewToolResultError("authz not configured. Call with action=configure first"), nil
	}

	limit := args.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	// Get low-priv session cookies and headers
	lowPrivRecord, err := backend.resolveSession(cfg.lowPrivSession)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("low-priv session error: %v", err)), nil
	}
	lowCookies := authzExtractCookies(lowPrivRecord)
	lowHeaders := authzExtractHeaders(lowPrivRecord)

	// Query traffic from projectDB
	if projectDB == nil || projectDB.db == nil {
		return mcp.NewToolResultError("project database not initialized"), nil
	}

	projectDB.mu.Lock()

	var query string
	var queryArgs []any
	if args.Host != "" {
		query = `SELECT id, "index", host, method, path, status, length, scheme, port FROM _data WHERE host LIKE ? ORDER BY "index" DESC LIMIT ?`
		queryArgs = []any{"%" + args.Host + "%", limit}
	} else {
		query = `SELECT id, "index", host, method, path, status, length, scheme, port FROM _data ORDER BY "index" DESC LIMIT ?`
		queryArgs = []any{limit}
	}

	rows, err := projectDB.db.Query(query, queryArgs...)
	if err != nil {
		projectDB.mu.Unlock()
		return mcp.NewToolResultError(fmt.Sprintf("query error: %v", err)), nil
	}

	type trafficEntry struct {
		id     string
		index  int
		host   string
		method string
		path   string
		status int
		length int
		scheme string
		port   string
	}

	var entries []trafficEntry
	for rows.Next() {
		var e trafficEntry
		if err := rows.Scan(&e.id, &e.index, &e.host, &e.method, &e.path, &e.status, &e.length, &e.scheme, &e.port); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	rows.Close()

	// Get raw requests
	type rawEntry struct {
		id      string
		request string
	}
	var rawEntries []rawEntry
	for _, e := range entries {
		var rawReq string
		err := projectDB.db.QueryRow(`SELECT request FROM _raw WHERE id = ?`, e.id).Scan(&rawReq)
		if err != nil {
			continue
		}
		rawEntries = append(rawEntries, rawEntry{id: e.id, request: rawReq})
	}
	projectDB.mu.Unlock()

	// Run authorization tests
	var results []authzResult
	for i, raw := range rawEntries {
		if i >= len(entries) {
			break
		}
		e := entries[i]

		// Skip static resources
		if authzIsStaticResource(e.path) {
			continue
		}

		// Rebuild request with low-priv cookies
		modifiedReq := authzSwapSession(raw.request, lowCookies, lowHeaders)

		// Determine target
		port := e.port
		if port == "" {
			if e.scheme == "https" {
				port = "443"
			} else {
				port = "80"
			}
		}
		tls := e.scheme == "https"
		targetURL := fmt.Sprintf("%s://%s:%s", e.scheme, e.host, port)

		// Send with low-priv session
		resp, err := backend.sendRepeaterLogic(&RepeaterSendRequest{
			Host:        e.host,
			Port:        port,
			TLS:         tls,
			Request:     modifiedReq,
			Timeout:     15,
			Url:         targetURL,
			Note:        fmt.Sprintf("authz-test:low-priv:%s", e.id),
			GeneratedBy: "ai/mcp/authz",
		})

		result := authzResult{
			RequestID:      e.id,
			Method:         e.method,
			Path:           e.path,
			Host:           e.host,
			OriginalStatus: e.status,
		}

		if err != nil {
			result.Detail = fmt.Sprintf("low-priv request failed: %v", err)
			result.AccessControl = "error"
		} else {
			result.LowPrivStatus = parseStatusCode(resp.Response)
			result.BodySimilarity = authzBodySimilarity(
				authzExtractBody(raw.request),
				authzExtractBody(resp.Response),
			)

			// Determine access control status
			if result.LowPrivStatus == result.OriginalStatus && result.BodySimilarity > 0.9 {
				result.AccessControl = "bypassed"
			} else if result.LowPrivStatus == 401 || result.LowPrivStatus == 403 {
				result.AccessControl = "enforced"
			} else {
				result.AccessControl = "partial"
				result.Detail = fmt.Sprintf("status changed %d->%d, similarity=%.2f", result.OriginalStatus, result.LowPrivStatus, result.BodySimilarity)
			}
		}

		// Optionally test unauthenticated
		if cfg.unauthenticated {
			noAuthReq := authzStripAuth(raw.request)
			unauthResp, err := backend.sendRepeaterLogic(&RepeaterSendRequest{
				Host:        e.host,
				Port:        port,
				TLS:         tls,
				Request:     noAuthReq,
				Timeout:     15,
				Url:         targetURL,
				Note:        fmt.Sprintf("authz-test:unauth:%s", e.id),
				GeneratedBy: "ai/mcp/authz",
			})
			if err == nil {
				result.UnauthStatus = parseStatusCode(unauthResp.Response)
			}
		}

		results = append(results, result)

		// Small delay to avoid overwhelming the target
		time.Sleep(100 * time.Millisecond)
	}

	// Store results
	authzResultsMu.Lock()
	authzResults = results
	authzResultsMu.Unlock()

	// Summarize
	bypassed := 0
	enforced := 0
	partial := 0
	for _, r := range results {
		switch r.AccessControl {
		case "bypassed":
			bypassed++
		case "enforced":
			enforced++
		case "partial":
			partial++
		}
	}

	return mcpJSONResult(map[string]any{
		"tested":   len(results),
		"bypassed": bypassed,
		"enforced": enforced,
		"partial":  partial,
		"results":  results,
	})
}

func (backend *Backend) authzResultsHandler(args AuthzTestArgs) (*mcp.CallToolResult, error) {
	authzResultsMu.Lock()
	results := make([]authzResult, len(authzResults))
	copy(results, authzResults)
	authzResultsMu.Unlock()

	if len(results) == 0 {
		return mcp.NewToolResultError("no results. Run authzTest with action=run first"), nil
	}

	return mcpJSONResult(map[string]any{
		"total":   len(results),
		"results": results,
	})
}

// ---------------------------------------------------------------------------
// Helpers — prefixed with authz to avoid collisions with other mcp_* files
// ---------------------------------------------------------------------------

func authzExtractCookies(record interface{ Get(string) any }) map[string]string {
	cookies, _ := record.Get("cookies").(map[string]any)
	if cookies == nil {
		return nil
	}
	result := make(map[string]string, len(cookies))
	for k, v := range cookies {
		result[k] = fmt.Sprintf("%v", v)
	}
	return result
}

func authzExtractHeaders(record interface{ Get(string) any }) map[string]string {
	headers, _ := record.Get("headers").(map[string]any)
	if headers == nil {
		return nil
	}
	result := make(map[string]string, len(headers))
	for k, v := range headers {
		result[k] = fmt.Sprintf("%v", v)
	}
	return result
}

func authzSwapSession(rawReq string, cookies map[string]string, headers map[string]string) string {
	lines := strings.Split(rawReq, "\r\n")
	if len(lines) == 0 {
		return rawReq
	}

	var result []string
	cookieReplaced := false

	for _, line := range lines {
		lower := strings.ToLower(line)
		// Replace Cookie header
		if strings.HasPrefix(lower, "cookie:") {
			if len(cookies) > 0 && !cookieReplaced {
				var cookieParts []string
				for k, v := range cookies {
					cookieParts = append(cookieParts, k+"="+v)
				}
				result = append(result, "Cookie: "+strings.Join(cookieParts, "; "))
				cookieReplaced = true
			}
			continue
		}
		// Replace Authorization header if low-priv has one
		if strings.HasPrefix(lower, "authorization:") {
			if auth, ok := headers["Authorization"]; ok {
				result = append(result, "Authorization: "+auth)
				continue
			}
			if auth, ok := headers["authorization"]; ok {
				result = append(result, "Authorization: "+auth)
				continue
			}
		}
		result = append(result, line)
	}

	return strings.Join(result, "\r\n")
}

func authzStripAuth(rawReq string) string {
	lines := strings.Split(rawReq, "\r\n")
	var result []string
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "cookie:") ||
			strings.HasPrefix(lower, "authorization:") {
			continue
		}
		result = append(result, line)
	}
	return strings.Join(result, "\r\n")
}

func authzIsStaticResource(path string) bool {
	staticExts := []string{
		".css", ".js", ".png", ".jpg", ".jpeg", ".gif",
		".svg", ".ico", ".woff", ".woff2", ".ttf", ".eot", ".map",
	}
	lower := strings.ToLower(path)
	for _, ext := range staticExts {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

func authzExtractBody(raw string) string {
	idx := strings.Index(raw, "\r\n\r\n")
	if idx >= 0 {
		return raw[idx+4:]
	}
	idx = strings.Index(raw, "\n\n")
	if idx >= 0 {
		return raw[idx+2:]
	}
	return ""
}

func authzBodySimilarity(body1, body2 string) float64 {
	if body1 == body2 {
		return 1.0
	}
	if len(body1) == 0 && len(body2) == 0 {
		return 1.0
	}
	if len(body1) == 0 || len(body2) == 0 {
		return 0.0
	}

	// Length-based ratio
	lenRatio := float64(min(len(body1), len(body2))) / float64(max(len(body1), len(body2)))

	// Line-based similarity: ratio of shared lines
	lines1 := strings.Split(body1, "\n")
	lines2 := strings.Split(body2, "\n")
	set1 := make(map[string]bool, len(lines1))
	for _, l := range lines1 {
		set1[strings.TrimSpace(l)] = true
	}
	common := 0
	for _, l := range lines2 {
		if set1[strings.TrimSpace(l)] {
			common++
		}
	}
	lineRatio := float64(common) / float64(max(len(lines1), len(lines2)))

	return (lenRatio + lineRatio) / 2
}
