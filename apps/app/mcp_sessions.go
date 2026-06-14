package app

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/campbellcharlie/lorg/internal/lorgdb"
	"github.com/mark3labs/mcp-go/mcp"
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

// project on session args scopes the jar to a project (ADR-002). Empty = the
// default project, matching pre-ADR-002 behavior.
type SessionCreateArgs struct {
	Name    string            `json:"name" jsonschema:"required" jsonschema_description:"Unique session name (e.g. admin, user1)"`
	Cookies map[string]string `json:"cookies,omitempty" jsonschema_description:"Initial cookies as name:value pairs"`
	Headers FlexibleStringMap `json:"headers,omitempty" jsonschema_description:"Persistent headers (e.g. Authorization: Bearer ...)"`
	Project string            `json:"project,omitempty" jsonschema_description:"Project this session belongs to (default: the default project). Lets each project keep its own cookie jar."`
}

type SessionListArgs struct{}

type SessionSwitchArgs struct {
	Name    string `json:"name" jsonschema:"required" jsonschema_description:"Session name to activate"`
	Project string `json:"project,omitempty" jsonschema_description:"Project the session belongs to (default: the default project)"`
}

type SessionDeleteArgs struct {
	Name    string `json:"name" jsonschema:"required" jsonschema_description:"Session name to delete"`
	Project string `json:"project,omitempty" jsonschema_description:"Project the session belongs to (default: the default project)"`
}

type SessionUpdateCookiesArgs struct {
	Name    string   `json:"name,omitempty" jsonschema_description:"Session name (default: active session)"`
	Cookies []string `json:"cookies" jsonschema:"required" jsonschema_description:"Set-Cookie header values or name=value pairs to merge"`
	Project string   `json:"project,omitempty" jsonschema_description:"Project the session belongs to (default: the default project)"`
}

type SessionGetHeadersArgs struct {
	Name    string `json:"name,omitempty" jsonschema_description:"Session name (default: active session)"`
	Project string `json:"project,omitempty" jsonschema_description:"Project the session belongs to (default: the default project)"`
}

type CsrfExtractArgs struct {
	Content        string   `json:"content" jsonschema:"required" jsonschema_description:"HTTP response body to extract CSRF tokens from"`
	CustomPatterns []string `json:"customPatterns,omitempty" jsonschema_description:"Additional regex patterns to try"`
	SessionName    string   `json:"sessionName,omitempty" jsonschema_description:"Session to store extracted token in"`
	Project        string   `json:"project,omitempty" jsonschema_description:"Project the target session belongs to (default: the default project)"`
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// Sessions are scoped to a project (ADR-002): the project="" rows are the
// "default project" jar, which is where every pre-ADR-002 session lives after
// migration. The *InProject helpers carry an explicit project; the bare
// wrappers default to "" so existing callers (authz, cookie, template tools)
// keep operating on the default project unchanged.

func (backend *Backend) findActiveSessionInProject(project string) (*lorgdb.Record, error) {
	record, err := backend.DB.FindFirstRecord("_sessions", "active = ? AND project = ?", true, project)
	if err != nil {
		return nil, fmt.Errorf("no active session for project %q, create one with sessionCreate and activate with sessionSwitch", project)
	}
	return record, nil
}

func (backend *Backend) findActiveSession() (*lorgdb.Record, error) {
	return backend.findActiveSessionInProject("")
}

func (backend *Backend) findSessionByNameInProject(project, name string) (*lorgdb.Record, error) {
	return backend.DB.FindFirstRecord("_sessions", "name = ? AND project = ?", name, project)
}

func (backend *Backend) findSessionByName(name string) (*lorgdb.Record, error) {
	return backend.findSessionByNameInProject("", name)
}

// resolveSessionInProject returns a session by name within a project, or that
// project's active session if name is empty.
func (backend *Backend) resolveSessionInProject(project, name string) (*lorgdb.Record, error) {
	if name != "" {
		return backend.findSessionByNameInProject(project, name)
	}
	return backend.findActiveSessionInProject(project)
}

// resolveSession returns a session by name (or the active session) in the
// default project. Thin wrapper kept for existing callers.
func (backend *Backend) resolveSession(name string) (*lorgdb.Record, error) {
	return backend.resolveSessionInProject("", name)
}

// copyCookiesBetween merges selected cookies from one (project, session) jar
// into another (ADR-002 slice 3). Empty fromName/toName resolves that project's
// active session. An empty names slice copies every cookie in the source jar;
// otherwise only the named cookies are copied. Returns the names actually copied
// plus the resolved source/destination session names. lorg's jar is a flat
// name->value map with no per-cookie domain, so this is a pure value merge — the
// caller decides whether the cookies are meaningful against the destination's
// target (e.g. shared IdP/SSO or bearer tokens).
func (backend *Backend) copyCookiesBetween(fromProject, fromName, toProject, toName string, names []string) (copied []string, srcName, dstName string, err error) {
	src, err := backend.resolveSessionInProject(fromProject, fromName)
	if err != nil {
		return nil, "", "", fmt.Errorf("source session not found (project %q, name %q): %w", fromProject, fromName, err)
	}
	dst, err := backend.resolveSessionInProject(toProject, toName)
	if err != nil {
		return nil, "", "", fmt.Errorf("destination session not found (project %q, name %q): %w", toProject, toName, err)
	}
	if src.Id == dst.Id {
		return nil, "", "", fmt.Errorf("source and destination are the same session")
	}

	srcCookies, _ := src.Get("cookies").(map[string]any)
	dstCookies, _ := dst.Get("cookies").(map[string]any)
	if dstCookies == nil {
		dstCookies = make(map[string]any)
	}

	allow := make(map[string]bool, len(names))
	for _, n := range names {
		allow[n] = true
	}

	copied = make([]string, 0, len(srcCookies))
	for k, v := range srcCookies {
		if len(allow) > 0 && !allow[k] {
			continue
		}
		dstCookies[k] = v
		copied = append(copied, k)
	}

	dst.Set("cookies", dstCookies)
	if err := backend.DB.SaveRecord(dst); err != nil {
		return nil, "", "", fmt.Errorf("failed to save destination jar: %w", err)
	}
	return copied, src.GetString("name"), dst.GetString("name"), nil
}

// ---------------------------------------------------------------------------
// Tool handlers
// ---------------------------------------------------------------------------

func (backend *Backend) sessionCreateHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args SessionCreateArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// Check if session with this name already exists IN THIS PROJECT. The same
	// name may exist in a different project (ADR-002 per-project jars).
	existing, _ := backend.findSessionByNameInProject(args.Project, args.Name)
	if existing != nil {
		return mcp.NewToolResultError(fmt.Sprintf("session with name %q already exists in project %q", args.Name, args.Project)), nil
	}

	record := lorgdb.NewRecord("_sessions")
	record.Set("name", args.Name)
	record.Set("project", args.Project)
	record.Set("cookies", args.Cookies)
	record.Set("headers", args.Headers)
	record.Set("active", false)

	if err := backend.DB.SaveRecord(record); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to create session: %v", err)), nil
	}

	return mcpJSONResult(map[string]any{
		"success":   true,
		"sessionId": record.Id,
		"name":      args.Name,
	})
}

func (backend *Backend) sessionListHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	records, err := backend.DB.FindRecords("_sessions", "1=1")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to list sessions: %v", err)), nil
	}

	sessions := make([]map[string]any, 0, len(records))
	for _, rec := range records {
		cookies, _ := rec.Get("cookies").(map[string]any)
		headers, _ := rec.Get("headers").(map[string]any)

		sessions = append(sessions, map[string]any{
			"name":        rec.GetString("name"),
			"project":     rec.GetString("project"),
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

	// Find the target session within its project.
	target, err := backend.findSessionByNameInProject(args.Project, args.Name)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("session not found: %s (project %q)", args.Name, args.Project)), nil
	}

	// Deactivate only sessions IN THE SAME PROJECT, so switching the active
	// session in project B leaves project A's active session untouched.
	projRecords, err := backend.DB.FindRecords("_sessions", "project = ?", args.Project)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to list sessions: %v", err)), nil
	}

	for _, rec := range projRecords {
		if rec.GetBool("active") {
			rec.Set("active", false)
			if err := backend.DB.SaveRecord(rec); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("failed to deactivate session %s: %v", rec.GetString("name"), err)), nil
			}
		}
	}

	// Activate the target session
	target.Set("active", true)
	if err := backend.DB.SaveRecord(target); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to activate session: %v", err)), nil
	}

	return mcpJSONResult(map[string]any{
		"success":       true,
		"activeSession": args.Name,
		"project":       args.Project,
	})
}

func (backend *Backend) sessionDeleteHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args SessionDeleteArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	record, err := backend.findSessionByNameInProject(args.Project, args.Name)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("session not found: %s (project %q)", args.Name, args.Project)), nil
	}

	if err := backend.DB.DeleteRecord("_sessions", record.Id); err != nil {
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

	record, err := backend.resolveSessionInProject(args.Project, args.Name)
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
	if err := backend.DB.SaveRecord(record); err != nil {
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

	record, err := backend.resolveSessionInProject(args.Project, args.Name)
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
		record, err := backend.findSessionByNameInProject(args.Project, args.SessionName)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("session not found: %s (project %q)", args.SessionName, args.Project)), nil
		}
		record.Set("csrf_token", tokens[0].Value)
		record.Set("csrf_field", tokens[0].Name)
		if err := backend.DB.SaveRecord(record); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to update session CSRF token: %v", err)), nil
		}
	}

	return mcpJSONResult(map[string]any{
		"tokens": tokens,
		"count":  len(tokens),
	})
}
