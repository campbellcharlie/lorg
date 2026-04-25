package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// Input schema
// ---------------------------------------------------------------------------

type GatherContextArgs struct {
	Host  string `json:"host,omitempty" jsonschema_description:"Target hostname to gather context for. Omit or pass empty to gather global stats across all hosts in the active project DB."`
	Limit int    `json:"limit,omitempty" jsonschema_description:"Max traffic entries to analyze (default 500)"`
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func (backend *Backend) gatherContextHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args GatherContextArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	limit := args.Limit
	if limit <= 0 {
		limit = 500
	}

	if projectDB == nil || projectDB.db == nil {
		return mcp.NewToolResultError("project database not initialized. Use project(action:'list') / project(action:'switch', name:'...') to activate a DB first."), nil
	}

	projectDB.mu.Lock()
	defer projectDB.mu.Unlock()

	// Build a where clause + args set we reuse across every query. Empty
	// host = global stats across the entire project DB.
	whereClause := "1=1"
	var whereArgs []any
	if args.Host != "" {
		whereClause = "host LIKE ?"
		whereArgs = append(whereArgs, "%"+args.Host+"%")
	}

	// 1. Unique endpoints (method + path)
	endpointSQL := fmt.Sprintf(
		"SELECT DISTINCT method, path FROM http_traffic WHERE %s LIMIT ?",
		whereClause,
	)
	endpointArgs := append(append([]any{}, whereArgs...), limit)
	endpointRows, err := projectDB.db.Query(endpointSQL, endpointArgs...)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("endpoint query error: %v", err)), nil
	}
	type endpoint struct {
		Method string `json:"method"`
		Path   string `json:"path"`
	}
	var endpoints []endpoint
	for endpointRows.Next() {
		var e endpoint
		if err := endpointRows.Scan(&e.Method, &e.Path); err == nil {
			endpoints = append(endpoints, e)
		}
	}
	endpointRows.Close()

	// 2. Status code distribution
	statusSQL := fmt.Sprintf(
		"SELECT status_code, COUNT(*) as cnt FROM http_traffic WHERE %s GROUP BY status_code ORDER BY cnt DESC",
		whereClause,
	)
	statusRows, err := projectDB.db.Query(statusSQL, whereArgs...)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("status query error: %v", err)), nil
	}
	statusDist := make(map[int]int)
	for statusRows.Next() {
		var status, count int
		if err := statusRows.Scan(&status, &count); err == nil {
			statusDist[status] = count
		}
	}
	statusRows.Close()

	// 3. Unique parameter names (from query strings)
	paramSQL := fmt.Sprintf(
		"SELECT DISTINCT query FROM http_traffic WHERE %s AND query IS NOT NULL AND query != '' LIMIT ?",
		whereClause,
	)
	paramArgs := append(append([]any{}, whereArgs...), limit)
	paramRows, err := projectDB.db.Query(paramSQL, paramArgs...)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("param query error: %v", err)), nil
	}
	paramSet := make(map[string]bool)
	for paramRows.Next() {
		var query string
		if err := paramRows.Scan(&query); err == nil {
			for _, pair := range strings.Split(query, "&") {
				if eqIdx := strings.IndexByte(pair, '='); eqIdx >= 0 {
					paramSet[pair[:eqIdx]] = true
				} else if pair != "" {
					paramSet[pair] = true
				}
			}
		}
	}
	paramRows.Close()
	params := make([]string, 0, len(paramSet))
	for p := range paramSet {
		params = append(params, p)
	}

	// 4. Total request count
	var totalRequests int
	countSQL := fmt.Sprintf("SELECT COUNT(*) FROM http_traffic WHERE %s", whereClause)
	_ = projectDB.db.QueryRow(countSQL, whereArgs...).Scan(&totalRequests)

	// 5. Content types (MIME distribution)
	mimeSQL := fmt.Sprintf(
		"SELECT mime_type, COUNT(*) as cnt FROM http_traffic WHERE %s AND mime_type != '' GROUP BY mime_type ORDER BY cnt DESC LIMIT 20",
		whereClause,
	)
	mimeRows, err := projectDB.db.Query(mimeSQL, whereArgs...)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("mime query error: %v", err)), nil
	}
	mimeDist := make(map[string]int)
	for mimeRows.Next() {
		var mime string
		var count int
		if err := mimeRows.Scan(&mime, &count); err == nil {
			mimeDist[mime] = count
		}
	}
	mimeRows.Close()

	// 6. Per-host counts when no host filter is set — gives the agent a
	// fast "what's in this DB" overview without needing a second tool call.
	var hostBreakdown []map[string]any
	if args.Host == "" {
		hostRows, hErr := projectDB.db.Query(
			`SELECT host, COUNT(*) as cnt FROM http_traffic GROUP BY host ORDER BY cnt DESC LIMIT 50`,
		)
		if hErr == nil {
			for hostRows.Next() {
				var h string
				var c int
				if hostRows.Scan(&h, &c) == nil {
					hostBreakdown = append(hostBreakdown, map[string]any{"host": h, "count": c})
				}
			}
			hostRows.Close()
		}
	}

	// 7. Technology stack from _hosts collection (lorgdb), only meaningful
	// when scoped to a single host.
	var technologies []string
	if args.Host != "" {
		hostRecord, hErr := backend.DB.FindFirstRecord("_hosts", "host = ?", args.Host)
		if hErr == nil && hostRecord != nil {
			if techRaw := hostRecord.Get("tech"); techRaw != nil {
				if techIDs, ok := techRaw.([]any); ok {
					for _, tid := range techIDs {
						if idStr, ok := tid.(string); ok {
							if techRec, techErr := backend.DB.FindRecordById("_tech", idStr); techErr == nil {
								technologies = append(technologies, techRec.GetString("name"))
							}
						}
					}
				}
			}
		}
	}

	// 8. Error signatures (4xx/5xx)
	errorSQL := fmt.Sprintf(
		"SELECT DISTINCT status_code, path FROM http_traffic WHERE %s AND status_code >= 400 ORDER BY status_code LIMIT 50",
		whereClause,
	)
	type errorSig struct {
		Status int    `json:"status"`
		Path   string `json:"path"`
	}
	var errors []errorSig
	if errorRows, eErr := projectDB.db.Query(errorSQL, whereArgs...); eErr == nil {
		for errorRows.Next() {
			var e errorSig
			if errorRows.Scan(&e.Status, &e.Path) == nil {
				errors = append(errors, e)
			}
		}
		errorRows.Close()
	}

	result := map[string]any{
		"host":               args.Host,
		"scope":              ifEmpty(args.Host, "global", "host"),
		"totalRequests":      totalRequests,
		"endpoints":          endpoints,
		"endpointCount":      len(endpoints),
		"statusDistribution": statusDist,
		"parameters":         params,
		"parameterCount":     len(params),
		"mimeTypes":          mimeDist,
		"technologies":       technologies,
		"errorSignatures":    errors,
	}
	if hostBreakdown != nil {
		result["hosts"] = hostBreakdown
	}
	return mcpJSONResult(result)
}

func ifEmpty(s, whenEmpty, whenNot string) string {
	if s == "" {
		return whenEmpty
	}
	return whenNot
}
