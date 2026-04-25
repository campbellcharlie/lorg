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

	"github.com/campbellcharlie/lorg/internal/lorgdb"
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

	// Build SQL WHERE clause with positional params
	var conditions []string
	var queryArgs []any

	if args.Host != "" {
		conditions = append(conditions, "host LIKE ?")
		queryArgs = append(queryArgs, "%"+args.Host+"%")
	}
	if args.Method != "" {
		conditions = append(conditions, "req_json LIKE ?")
		queryArgs = append(queryArgs, "%"+args.Method+"%")
	}
	if args.Path != "" {
		conditions = append(conditions, "req_json LIKE ?")
		queryArgs = append(queryArgs, "%"+args.Path+"%")
	}
	if args.Status != 0 {
		conditions = append(conditions, "resp_json LIKE ?")
		queryArgs = append(queryArgs, "%"+fmt.Sprintf("%d", args.Status)+"%")
	}

	where := "1=1"
	if len(conditions) > 0 {
		where = strings.Join(conditions, " AND ")
	}

	// Fetch records. When Query is set, fetch a larger batch to filter in-memory.
	fetchLimit := args.Limit
	if args.Query != "" {
		fetchLimit = args.Limit * 5
		if fetchLimit > 1000 {
			fetchLimit = 1000
		}
	}

	recs, err := backend.DB.FindRecordsSorted("_data", where, `"index" DESC`, fetchLimit, args.Offset, queryArgs...)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to search traffic: %v", err)), nil
	}

	records := wrapRecords(recs)

	// If Query is provided, filter by raw content match in _req/_resp
	if args.Query != "" {
		var filtered []trafficSearchRecord
		for _, dr := range records {
			if len(filtered) >= args.Limit {
				break
			}
			id := dr.GetString("id")
			reqRec, _ := backend.DB.FindRecordById("_req", id)
			respRec, _ := backend.DB.FindRecordById("_resp", id)

			matched := false
			if reqRec != nil && strings.Contains(reqRec.GetString("raw"), args.Query) {
				matched = true
			}
			if !matched && respRec != nil && strings.Contains(respRec.GetString("raw"), args.Query) {
				matched = true
			}
			if matched {
				filtered = append(filtered, dr)
			}
		}
		records = filtered
	}

	items := make([]map[string]any, 0, len(records))
	for _, dr := range records {
		reqJSON := asMap(dr.Get("req_json"))
		respJSON := asMap(dr.Get("resp_json"))

		method := mapStr(reqJSON, "method")
		path := mapStr(reqJSON, "path")
		status := int(mapFloat(respJSON, "status"))
		length := int(mapFloat(respJSON, "length"))

		items = append(items, map[string]any{
			"id":          dr.GetString("id"),
			"index":       dr.GetFloat("index"),
			"host":        dr.GetString("host"),
			"method":      method,
			"path":        path,
			"status":      status,
			"length":      length,
			"generatedBy": dr.GetString("generated_by"),
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

	dataRecords, err := backend.DB.FindRecordsSorted("_data", where, `"index" DESC`, fetchLimit, 0, queryArgs...)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to fetch data records: %v", err)), nil
	}

	items := make([]map[string]any, 0, limit)
	for _, rec := range dataRecords {
		if len(items) >= limit {
			break
		}
		id := rec.GetString("id")
		hostVal := rec.GetString("host")
		matchContext := ""

		if source == "request" || source == "both" {
			reqRec, _ := backend.DB.FindRecordById("_req", id)
			if reqRec != nil {
				raw := reqRec.GetString("raw")
				loc := re.FindStringIndex(raw)
				if loc != nil {
					matchContext = extractRegexMatchContext(raw, loc[0], 200)
				}
			}
		}
		if matchContext == "" && (source == "response" || source == "both") {
			respRec, _ := backend.DB.FindRecordById("_resp", id)
			if respRec != nil {
				raw := respRec.GetString("raw")
				loc := re.FindStringIndex(raw)
				if loc != nil {
					matchContext = extractRegexMatchContext(raw, loc[0], 200)
				}
			}
		}
		if matchContext != "" {
			items = append(items, map[string]any{
				"id":           id,
				"host":         hostVal,
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

// trafficSearchRecord wraps a lorgdb.Record for use in search handlers.
type trafficSearchRecord struct {
	inner interface {
		GetString(key string) string
		GetFloat(key string) float64
		Get(key string) any
	}
}

func (t trafficSearchRecord) GetString(key string) string { return t.inner.GetString(key) }
func (t trafficSearchRecord) GetFloat(key string) float64  { return t.inner.GetFloat(key) }
func (t trafficSearchRecord) Get(key string) any           { return t.inner.Get(key) }

// wrapRecords converts a slice of *lorgdb.Record into []trafficSearchRecord.
func wrapRecords(recs []*lorgdb.Record) []trafficSearchRecord {
	out := make([]trafficSearchRecord, len(recs))
	for i, r := range recs {
		out[i] = trafficSearchRecord{r}
	}
	return out
}

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

// parseReqJSONString unmarshals a JSON string from a raw SQL result into a map.
func parseReqJSONString(raw string) map[string]any {
	if raw == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	return m
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

	var sql string
	var queryArgs []any

	if args.HostFilter != "" {
		sql = `SELECT req_json FROM _data WHERE host LIKE ? ORDER BY "index" DESC`
		queryArgs = append(queryArgs, "%"+args.HostFilter+"%")
	} else {
		sql = `SELECT req_json FROM _data ORDER BY "index" DESC`
	}

	rows, err := backend.DB.Query(sql, queryArgs...)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to query traffic: %v", err)), nil
	}
	defer rows.Close()

	unique := make(map[string]bool)

	for rows.Next() {
		var reqJSON string
		if err := rows.Scan(&reqJSON); err != nil {
			continue
		}
		parsed := parseReqJSONString(reqJSON)
		if parsed == nil {
			continue
		}

		// Extract path segments
		if args.Source == "paths" || args.Source == "both" {
			pathStr := mapStr(parsed, "path")
			if pathStr != "" {
				segments := strings.Split(pathStr, "/")
				for _, seg := range segments {
					seg = strings.TrimSpace(seg)
					if seg != "" {
						unique[seg] = true
					}
				}
			}
		}

		// Extract parameter names
		if args.Source == "parameters" || args.Source == "both" {
			queryStr := mapStr(parsed, "query")
			if queryStr != "" {
				values, err := url.ParseQuery(queryStr)
				if err == nil {
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
