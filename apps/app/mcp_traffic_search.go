package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// Input schemas
// ---------------------------------------------------------------------------

type SearchTrafficArgs struct {
	Host        string `json:"host,omitempty" jsonschema_description:"Filter by host (substring match)"`
	Path        string `json:"path,omitempty" jsonschema_description:"Filter by URL path substring"`
	Method      string `json:"method,omitempty" jsonschema_description:"Filter by HTTP method"`
	Status      int    `json:"status,omitempty" jsonschema_description:"Filter by response status code"`
	Query       string `json:"query,omitempty" jsonschema_description:"Search in request/response raw content (substring by default; regex when regex=true)"`
	Regex       bool   `json:"regex,omitempty" jsonschema_description:"Treat query as a Go regex pattern instead of a literal substring"`
	RegexSource string `json:"regexSource,omitempty" jsonschema_description:"For regex queries, which side to search: request, response, or both (default: both)"`
	Project     string `json:"project,omitempty" jsonschema_description:"Filter by project tag (matches the project column on captured rows). Independent from the UI's currently-viewed project — the agent can scope reads to any project regardless of what the user has on screen."`
	Limit       int    `json:"limit" jsonschema:"required" jsonschema_description:"Max results (max 200)"`
	Offset      int    `json:"offset,omitempty" jsonschema_description:"Offset for pagination (cursor-style)"`
}

// ---------------------------------------------------------------------------
// Tool handlers
// ---------------------------------------------------------------------------

func (backend *Backend) searchTrafficHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args SearchTrafficArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	if args.Limit <= 0 || args.Limit > 200 {
		args.Limit = 200
	}

	// Regex mode delegates to the existing regex handler — same engine,
	// just exposed through the unified tool surface.
	if args.Regex {
		if args.Query == "" {
			return mcp.NewToolResultError("query is required when regex=true. Pass a Go regex pattern, e.g. \"X-[A-Za-z-]+\""), nil
		}
		src := args.RegexSource
		if src == "" {
			src = "both"
		}
		return backend.runTrafficRegexSearch(args.Host, args.Query, src, args.Limit)
	}

	// Raw-content search (args.Query) still needs _req/_resp bytes — handled on
	// the legacy path until E7 unifies byte access. Metadata-only search uses the
	// cross-project union read layer (ADR-004 E3).
	if args.Query != "" {
		return backend.searchTrafficRawContent(args)
	}

	// Metadata filters map directly to http_traffic's flat columns.
	var conditions []string
	var queryArgs []any
	if args.Host != "" {
		conditions = append(conditions, "host LIKE ?")
		queryArgs = append(queryArgs, "%"+args.Host+"%")
	}
	if args.Method != "" {
		conditions = append(conditions, "method = ?")
		queryArgs = append(queryArgs, args.Method)
	}
	if args.Path != "" {
		conditions = append(conditions, "path LIKE ?")
		queryArgs = append(queryArgs, "%"+args.Path+"%")
	}
	if args.Status != 0 {
		conditions = append(conditions, "status_code = ?")
		queryArgs = append(queryArgs, args.Status)
	}
	where := strings.Join(conditions, " AND ")

	rows, err := projectDB.unionTrafficRows(args.Project, where, queryArgs, args.Limit+args.Offset)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to search traffic: %v", err)), nil
	}
	if args.Offset > 0 && args.Offset < len(rows) {
		rows = rows[args.Offset:]
	} else if args.Offset >= len(rows) {
		rows = nil
	}

	items := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		items = append(items, map[string]any{
			"id":          makeRowID(r.Project, r.RequestID),
			"project":     r.Project,
			"index":       r.GlobalSeq,
			"host":        r.Host,
			"method":      r.Method,
			"path":        r.Path,
			"status":      r.Status,
			"length":      r.RespLength,
			"generatedBy": r.GeneratedBy,
		})
	}

	return mcpJSONResult(map[string]any{
		"totalItems": len(items),
		"items":      items,
	})
}

// searchTrafficRawContent is the legacy raw-content (args.Query substring) path,
// still reading _data + _req/_resp. Migrated to http_messages in E7.
func (backend *Backend) searchTrafficRawContent(args SearchTrafficArgs) (*mcp.CallToolResult, error) {
	var conditions []string
	var queryArgs []any
	if args.Host != "" {
		conditions = append(conditions, "host LIKE ?")
		queryArgs = append(queryArgs, "%"+args.Host+"%")
	}
	where := strings.Join(conditions, " AND ")

	fetchLimit := args.Limit * 5
	if fetchLimit > 1000 {
		fetchLimit = 1000
	}
	urows, err := projectDB.unionTrafficRows(args.Project, where, queryArgs, fetchLimit)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to search traffic: %v", err)), nil
	}

	items := make([]map[string]any, 0, args.Limit)
	for _, r := range urows {
		if len(items) >= args.Limit {
			break
		}
		reqRaw, respRaw, _ := projectDB.getTrafficBytes(r.Project, r.RequestID)
		if !strings.Contains(reqRaw, args.Query) && !strings.Contains(respRaw, args.Query) {
			continue
		}
		items = append(items, map[string]any{
			"id":          makeRowID(r.Project, r.RequestID),
			"project":     r.Project,
			"index":       r.GlobalSeq,
			"host":        r.Host,
			"method":      r.Method,
			"path":        r.Path,
			"status":      r.Status,
			"length":      r.RespLength,
			"generatedBy": r.GeneratedBy,
		})
	}

	return mcpJSONResult(map[string]any{
		"totalItems": len(items),
		"items":      items,
	})
}

// runTrafficRegexSearch is the regex-mode body of searchTraffic. Kept as
// a separate function so the unified searchTrafficHandler can delegate to
// it cleanly.
func (backend *Backend) runTrafficRegexSearch(host, pattern, source string, limit int) (*mcp.CallToolResult, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	if source != "request" && source != "response" && source != "both" {
		return mcp.NewToolResultError("regexSource must be 'request', 'response', or 'both'. Got: " + source), nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid regex pattern: %v. Example: \"X-[A-Za-z-]+\"", err)), nil
	}

	where := "1=1"
	var queryArgs []any
	if host != "" {
		where = "host LIKE ?"
		queryArgs = append(queryArgs, "%"+host+"%")
	}

	fetchLimit := limit * 10
	if fetchLimit > 2000 {
		fetchLimit = 2000
	}

	urows, err := projectDB.unionTrafficRows("", where, queryArgs, fetchLimit)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to fetch data records: %v", err)), nil
	}

	items := make([]map[string]any, 0, limit)
	for _, r := range urows {
		if len(items) >= limit {
			break
		}
		reqRaw, respRaw, _ := projectDB.getTrafficBytes(r.Project, r.RequestID)
		matchContext := ""
		if source == "request" || source == "both" {
			if loc := re.FindStringIndex(reqRaw); loc != nil {
				matchContext = extractRegexMatchContext(reqRaw, loc[0], 200)
			}
		}
		if matchContext == "" && (source == "response" || source == "both") {
			if loc := re.FindStringIndex(respRaw); loc != nil {
				matchContext = extractRegexMatchContext(respRaw, loc[0], 200)
			}
		}
		if matchContext != "" {
			items = append(items, map[string]any{
				"id":           makeRowID(r.Project, r.RequestID),
				"host":         r.Host,
				"matchContext": matchContext,
			})
		}
	}

	return mcpJSONResult(map[string]any{
		"totalItems": len(items),
		"items":      items,
		"hasMore":    len(items) >= limit,
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// extractRegexMatchContext returns up to maxLen characters of context around the match position.
func extractRegexMatchContext(raw string, matchStart int, maxLen int) string {
	halfCtx := maxLen / 2
	start := matchStart - halfCtx
	if start < 0 {
		start = 0
	}
	end := start + maxLen
	if end > len(raw) {
		end = len(raw)
	}
	return raw[start:end]
}

// asMap safely type-asserts a value to map[string]any.
func asMap(v any) map[string]any {
	if v == nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	// Handle JSON-encoded strings stored in the database.
	if s, ok := v.(string); ok && len(s) > 0 && s[0] == '{' {
		var m map[string]any
		if json.Unmarshal([]byte(s), &m) == nil {
			return m
		}
	}
	return nil
}

// mapStr extracts a string value from a map.
func mapStr(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// mapFloat extracts a float64 value from a map.
func mapFloat(m map[string]any, key string) float64 {
	if m == nil {
		return 0
	}
	if v, ok := m[key].(float64); ok {
		return v
	}
	return 0
}

// ---------------------------------------------------------------------------
// generateWordlist: extract paths/parameters from traffic into a wordlist
// ---------------------------------------------------------------------------

type GenerateWordlistArgs struct {
	Source     string `json:"source" jsonschema:"required" jsonschema_description:"What to extract: paths, parameters, or both"`
	HostFilter string `json:"hostFilter,omitempty" jsonschema_description:"Only extract from this host"`
	Project    string `json:"project,omitempty" jsonschema_description:"Optional project tag filter — restrict the wordlist source to traffic captured under this project. Independent from the UI's currently-viewed project."`
	OutputPath string `json:"outputPath" jsonschema:"required" jsonschema_description:"File path to write the wordlist to"`
}

func (backend *Backend) generateWordlistHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args GenerateWordlistArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	if args.Source != "paths" && args.Source != "parameters" && args.Source != "both" {
		return mcp.NewToolResultError("source must be 'paths', 'parameters', or 'both'"), nil
	}

	// Read path/query directly from http_traffic across all project DBs
	// (ADR-004 E8) — no more req_json parsing.
	where := ""
	var wargs []any
	if args.HostFilter != "" {
		where = "host LIKE ?"
		wargs = []any{"%" + args.HostFilter + "%"}
	}
	rows, err := projectDB.unionTrafficRows(args.Project, where, wargs, 0)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to query traffic: %v", err)), nil
	}

	unique := make(map[string]bool)
	for _, r := range rows {
		if args.Source == "paths" || args.Source == "both" {
			for _, seg := range strings.Split(r.Path, "/") {
				if seg = strings.TrimSpace(seg); seg != "" {
					unique[seg] = true
				}
			}
		}
		if args.Source == "parameters" || args.Source == "both" {
			if r.Query != "" {
				if values, perr := url.ParseQuery(r.Query); perr == nil {
					for name := range values {
						if name != "" {
							unique[name] = true
						}
					}
				}
			}
		}
	}

	// Deduplicate into a sorted slice
	wordlist := make([]string, 0, len(unique))
	for word := range unique {
		wordlist = append(wordlist, word)
	}
	sort.Strings(wordlist)

	// Write one entry per line
	content := strings.Join(wordlist, "\n")
	if len(wordlist) > 0 {
		content += "\n"
	}
	if err := os.WriteFile(args.OutputPath, []byte(content), 0644); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to write wordlist: %v", err)), nil
	}

	return mcpJSONResult(map[string]any{
		"success":    true,
		"outputPath": args.OutputPath,
		"wordCount":  len(wordlist),
		"source":     args.Source,
	})
}
