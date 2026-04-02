package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/pocketbase/dbx"
)

// ---------------------------------------------------------------------------
// Input schema
// ---------------------------------------------------------------------------

type GatherContextArgs struct {
	Host  string `json:"host" jsonschema:"required" jsonschema_description:"Target hostname to gather context for"`
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

	if args.Host == "" {
		return mcp.NewToolResultError("host is required"), nil
	}

	limit := args.Limit
	if limit <= 0 {
		limit = 500
	}

	if projectDB == nil || projectDB.db == nil {
		return mcp.NewToolResultError("project database not initialized"), nil
	}

	projectDB.mu.Lock()
	defer projectDB.mu.Unlock()

	hostPattern := "%" + args.Host + "%"

	// 1. Get unique endpoints
	endpointRows, err := projectDB.db.Query(
		`SELECT DISTINCT method, path FROM _data WHERE host LIKE ? LIMIT ?`,
		hostPattern, limit,
	)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("query error: %v", err)), nil
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
	statusRows, err := projectDB.db.Query(
		`SELECT status, COUNT(*) as count FROM _data WHERE host LIKE ? GROUP BY status ORDER BY count DESC`,
		hostPattern,
	)
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

	// 3. Unique parameters (from query strings)
	paramRows, err := projectDB.db.Query(
		`SELECT DISTINCT path FROM _data WHERE host LIKE ? AND path LIKE '%?%' LIMIT ?`,
		hostPattern, limit,
	)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("param query error: %v", err)), nil
	}
	paramSet := make(map[string]bool)
	for paramRows.Next() {
		var path string
		if err := paramRows.Scan(&path); err == nil {
			if qIdx := strings.IndexByte(path, '?'); qIdx >= 0 {
				query := path[qIdx+1:]
				for _, pair := range strings.Split(query, "&") {
					if eqIdx := strings.IndexByte(pair, '='); eqIdx >= 0 {
						paramSet[pair[:eqIdx]] = true
					} else {
						paramSet[pair] = true
					}
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
	_ = projectDB.db.QueryRow(
		`SELECT COUNT(*) FROM _data WHERE host LIKE ?`,
		hostPattern,
	).Scan(&totalRequests)

	// 5. Content types (MIME distribution)
	mimeRows, err := projectDB.db.Query(
		`SELECT mime, COUNT(*) as count FROM _data WHERE host LIKE ? AND mime != '' GROUP BY mime ORDER BY count DESC LIMIT 20`,
		hostPattern,
	)
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

	// 6. Technology stack from PocketBase _hosts collection (if available)
	var technologies []string
	dao := backend.App.Dao()
	hostRecord, err := dao.FindFirstRecordByFilter("_hosts", "host = {:host}", dbx.Params{"host": args.Host})
	if err == nil && hostRecord != nil {
		dao.ExpandRecord(hostRecord, []string{"tech"}, nil)
		for _, t := range hostRecord.ExpandedAll("tech") {
			technologies = append(technologies, t.GetString("name"))
		}
	}

	// 7. Error signatures (4xx/5xx responses with distinct paths)
	errorRows, err := projectDB.db.Query(
		`SELECT DISTINCT status, path FROM _data WHERE host LIKE ? AND status >= 400 ORDER BY status LIMIT 50`,
		hostPattern,
	)
	if err != nil {
		// Return results without error signatures
		return mcpJSONResult(map[string]any{
			"host":               args.Host,
			"totalRequests":      totalRequests,
			"endpoints":          endpoints,
			"endpointCount":      len(endpoints),
			"statusDistribution": statusDist,
			"parameters":         params,
			"parameterCount":     len(params),
			"mimeTypes":          mimeDist,
			"technologies":       technologies,
		})
	}
	type errorSig struct {
		Status int    `json:"status"`
		Path   string `json:"path"`
	}
	var errors []errorSig
	for errorRows.Next() {
		var e errorSig
		if errorRows.Scan(&e.Status, &e.Path) == nil {
			errors = append(errors, e)
		}
	}
	errorRows.Close()

	return mcpJSONResult(map[string]any{
		"host":               args.Host,
		"totalRequests":      totalRequests,
		"endpoints":          endpoints,
		"endpointCount":      len(endpoints),
		"statusDistribution": statusDist,
		"parameters":         params,
		"parameterCount":     len(params),
		"mimeTypes":          mimeDist,
		"technologies":       technologies,
		"errorSignatures":    errors,
	})
}
