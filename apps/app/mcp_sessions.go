package app

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/glitchedgitz/pocketbase/models"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/pocketbase/dbx"
)

// FlexibleStringMap is a map[string]string that accepts both a JSON object
// and a JSON-encoded string (double-encoded). Some MCP clients serialize
// map parameters as a JSON string rather than a nested object.
type FlexibleStringMap map[string]string

func (m *FlexibleStringMap) UnmarshalJSON(data []byte) error {
	// Try direct object unmarshaling first
	var direct map[string]string
	if err := json.Unmarshal(data, &direct); err == nil {
		*m = direct
		return nil
	}
	// Fallback: value is a JSON-encoded string containing a JSON object
	var encoded string
	if err := json.Unmarshal(data, &encoded); err == nil {
		var decoded map[string]string
		if err2 := json.Unmarshal([]byte(encoded), &decoded); err2 == nil {
			*m = decoded
			return nil
		}
	}
	return fmt.Errorf("headers: expected a JSON object or a JSON-encoded object string")
}

// ---------------------------------------------------------------------------
// Input schemas
// ---------------------------------------------------------------------------

type SessionCreateArgs struct {
	Name    string            `json:"name" jsonschema:"required" jsonschema_description:"Unique session name (e.g. admin, user1)"`
	Cookies map[string]string `json:"cookies,omitempty" jsonschema_description:"Initial cookies as name:value pairs"`
	Headers FlexibleStringMap `json:"headers,omitempty" jsonschema_description:"Persistent headers (e.g. Authorization: Bearer ...)"`
}

type SessionListArgs struct{}

type SessionSwitchArgs struct {
	Name string `json:"name" jsonschema:"required" jsonschema_description:"Session name to activate"`
}

type SessionDeleteArgs struct {
	Name string `json:"name" jsonschema:"required" jsonschema_description:"Session name to delete"`
}

type SessionUpdateCookiesArgs struct {
	Name    string   `json:"name,omitempty" jsonschema_description:"Session name (default: active session)"`
	Cookies []string `json:"cookies" jsonschema:"required" jsonschema_description:"Set-Cookie header values or name=value pairs to merge"`
}

type SessionGetHeadersArgs struct {
	Name string `json:"name,omitempty" jsonschema_description:"Session name (default: active session)"`
}

type CsrfExtractArgs struct {
	Content        string   `json:"content" jsonschema:"required" jsonschema_description:"HTTP response body to extract CSRF tokens from"`
	CustomPatterns []string `json:"customPatterns,omitempty" jsonschema_description:"Additional regex patterns to try"`
	SessionName    string   `json:"sessionName,omitempty" jsonschema_description:"Session to store extracted token in"`
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (backend *Backend) findActiveSession() (*models.Record, error) {
	dao := backend.App.Dao()
	record, err := dao.FindFirstRecordByFilter("_sessions", "active = true")
	if err != nil {
		return nil, fmt.Errorf("no active session found, create one with sessionCreate and activate with sessionSwitch")
	}
	return record, nil
}

func (backend *Backend) findSessionByName(name string) (*models.Record, error) {
	dao := backend.App.Dao()
	return dao.FindFirstRecordByFilter("_sessions", "name = {:name}", dbx.Params{"name": name})
}

// resolveSession returns a session by name, or the active session if name is empty.
func (backend *Backend) resolveSession(name string) (*models.Record, error) {
	if name != "" {
		return backend.findSessionByName(name)
	}
	return backend.findActiveSession()
}

// ---------------------------------------------------------------------------
// Tool handlers
// ---------------------------------------------------------------------------

func (backend *Backend) sessionCreateHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args SessionCreateArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	dao := backend.App.Dao()

	// Check if session with this name already exists
	existing, _ := dao.FindFirstRecordByFilter("_sessions", "name = {:name}", dbx.Params{"name": args.Name})
	if existing != nil {
		return mcp.NewToolResultError(fmt.Sprintf("session with name %q already exists", args.Name)), nil
	}

	collection, err := dao.FindCollectionByNameOrId("_sessions")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to find _sessions collection: %v", err)), nil
	}

	record := models.NewRecord(collection)
	record.Set("name", args.Name)
	record.Set("cookies", args.Cookies)
	record.Set("headers", args.Headers)
	record.Set("active", false)

	if err := dao.SaveRecord(record); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to create session: %v", err)), nil
	}

	return mcpJSONResult(map[string]any{
		"success":   true,
		"sessionId": record.Id,
		"name":      args.Name,
	})
}

func (backend *Backend) sessionListHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	dao := backend.App.Dao()

	records, err := dao.FindRecordsByExpr("_sessions")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to list sessions: %v", err)), nil
	}

	sessions := make([]map[string]any, 0, len(records))
	for _, rec := range records {
		cookies, _ := rec.Get("cookies").(map[string]any)
		headers, _ := rec.Get("headers").(map[string]any)

		sessions = append(sessions, map[string]any{
			"name":        rec.GetString("name"),
			"active":      rec.GetBool("active"),
			"cookieCount": len(cookies),
			"headerCount": len(headers),
		})
	}

	return mcpJSONResult(map[string]any{
		"sessions": sessions,
		"count":    len(sessions),
	})
}

func (backend *Backend) sessionSwitchHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args SessionSwitchArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	dao := backend.App.Dao()

	// Find the target session
	target, err := dao.FindFirstRecordByFilter("_sessions", "name = {:name}", dbx.Params{"name": args.Name})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("session not found: %s", args.Name)), nil
	}

	// Deactivate all sessions
	allRecords, err := dao.FindRecordsByExpr("_sessions")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to list sessions: %v", err)), nil
	}

	for _, rec := range allRecords {
		if rec.GetBool("active") {
			rec.Set("active", false)
			if err := dao.SaveRecord(rec); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("failed to deactivate session %s: %v", rec.GetString("name"), err)), nil
			}
		}
	}

	// Activate the target session
	target.Set("active", true)
	if err := dao.SaveRecord(target); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to activate session: %v", err)), nil
	}

	return mcpJSONResult(map[string]any{
		"success":       true,
		"activeSession": args.Name,
	})
}

func (backend *Backend) sessionDeleteHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args SessionDeleteArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	dao := backend.App.Dao()

	record, err := dao.FindFirstRecordByFilter("_sessions", "name = {:name}", dbx.Params{"name": args.Name})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("session not found: %s", args.Name)), nil
	}

	if err := dao.DeleteRecord(record); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to delete session: %v", err)), nil
	}

	return mcpJSONResult(map[string]any{
		"success": true,
		"deleted": args.Name,
	})
}

func (backend *Backend) sessionUpdateCookiesHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args SessionUpdateCookiesArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	record, err := backend.resolveSession(args.Name)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// Load existing cookies
	existing, _ := record.Get("cookies").(map[string]any)
	if existing == nil {
		existing = make(map[string]any)
	}

	// Parse and merge each cookie string
	for _, raw := range args.Cookies {
		// Extract the name=value portion (text before the first ";")
		cookiePart := raw
		if idx := strings.Index(raw, ";"); idx != -1 {
			cookiePart = raw[:idx]
		}
		cookiePart = strings.TrimSpace(cookiePart)

		eqIdx := strings.Index(cookiePart, "=")
		if eqIdx == -1 {
			continue // skip malformed entries
		}
		name := strings.TrimSpace(cookiePart[:eqIdx])
		value := strings.TrimSpace(cookiePart[eqIdx+1:])
		existing[name] = value
	}

	record.Set("cookies", existing)
	dao := backend.App.Dao()
	if err := dao.SaveRecord(record); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to update cookies: %v", err)), nil
	}

	return mcpJSONResult(map[string]any{
		"success": true,
		"session": record.GetString("name"),
		"cookies": existing,
	})
}

func (backend *Backend) sessionGetHeadersHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args SessionGetHeadersArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	record, err := backend.resolveSession(args.Name)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	cookies, _ := record.Get("cookies").(map[string]any)
	headers, _ := record.Get("headers").(map[string]any)

	// Build Cookie header string: name1=val1; name2=val2
	var cookiePairs []string
	for k, v := range cookies {
		cookiePairs = append(cookiePairs, fmt.Sprintf("%s=%v", k, v))
	}
	cookieHeader := strings.Join(cookiePairs, "; ")

	return mcpJSONResult(map[string]any{
		"session":      record.GetString("name"),
		"cookieHeader": cookieHeader,
		"headers":      headers,
		"cookies":      cookies,
	})
}

func (backend *Backend) csrfExtractHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args CsrfExtractArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	type csrfToken struct {
		Name   string `json:"name"`
		Value  string `json:"value"`
		Source string `json:"source"`
	}

	var tokens []csrfToken

	// Common CSRF field names used across the patterns
	csrfNames := `csrf[_\-]?token|_token|csrfmiddlewaretoken|__RequestVerificationToken|_csrf|authenticity_token`

	// Pattern 1: <input ... name="csrf_token" ... value="...">
	re1 := regexp.MustCompile(`(?i)<input[^>]*name=["']?(` + csrfNames + `)["']?[^>]*value=["']?([^"'\s>]+)`)
	for _, m := range re1.FindAllStringSubmatch(args.Content, -1) {
		tokens = append(tokens, csrfToken{Name: m[1], Value: m[2], Source: "hidden_input"})
	}

	// Pattern 2: <input ... value="..." ... name="csrf_token"> (reversed attribute order)
	re2 := regexp.MustCompile(`(?i)<input[^>]*value=["']?([^"'\s>]+)["']?[^>]*name=["']?(` + csrfNames + `)`)
	for _, m := range re2.FindAllStringSubmatch(args.Content, -1) {
		tokens = append(tokens, csrfToken{Name: m[2], Value: m[1], Source: "hidden_input"})
	}

	// Pattern 3: <meta name="csrf-token" content="...">
	re3 := regexp.MustCompile(`(?i)<meta[^>]*name=["']?csrf-token["']?[^>]*content=["']?([^"']+)`)
	for _, m := range re3.FindAllStringSubmatch(args.Content, -1) {
		tokens = append(tokens, csrfToken{Name: "csrf-token", Value: m[1], Source: "meta_tag"})
	}

	// Pattern 4: <meta content="..." name="csrf-token"> (reversed attribute order)
	re4 := regexp.MustCompile(`(?i)<meta[^>]*content=["']?([^"']+)["']?[^>]*name=["']?csrf-token`)
	for _, m := range re4.FindAllStringSubmatch(args.Content, -1) {
		tokens = append(tokens, csrfToken{Name: "csrf-token", Value: m[1], Source: "meta_tag"})
	}

	// Custom patterns provided by the caller
	for _, pattern := range args.CustomPatterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue // skip invalid patterns
		}
		for _, m := range re.FindAllStringSubmatch(args.Content, -1) {
			if len(m) >= 2 {
				name := "custom"
				value := m[1]
				if len(m) >= 3 {
					name = m[1]
					value = m[2]
				}
				tokens = append(tokens, csrfToken{Name: name, Value: value, Source: "custom"})
			}
		}
	}

	// If tokens found and a session name is provided, store the first token in the session
	if len(tokens) > 0 && args.SessionName != "" {
		record, err := backend.findSessionByName(args.SessionName)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("session not found: %s", args.SessionName)), nil
		}
		record.Set("csrf_token", tokens[0].Value)
		record.Set("csrf_field", tokens[0].Name)
		dao := backend.App.Dao()
		if err := dao.SaveRecord(record); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to update session CSRF token: %v", err)), nil
		}
	}

	return mcpJSONResult(map[string]any{
		"tokens": tokens,
		"count":  len(tokens),
	})
}
