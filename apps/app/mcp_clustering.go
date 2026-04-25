package app

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// Cheap response clustering — backed by the fingerprint column on _data.
//
// Two MCP tools live here:
//   - clusterResponses: group responses by fingerprint, return summary buckets
//   - findAnomalies:    on a single endpoint, surface responses whose
//                       fingerprint differs from the modal one
//
// Both query backend.DB._data (the lorgdb store, source of truth for all
// proxy + MCP-tool traffic). They use json_extract to pull method/path out
// of the req_json blob since _data does not store those as columns.
// ---------------------------------------------------------------------------

type ClusterResponsesArgs struct {
	Host   string `json:"host,omitempty" jsonschema_description:"Hostname filter (LIKE substring match). Optional."`
	Method string `json:"method,omitempty" jsonschema_description:"HTTP method filter (e.g. GET, POST). Optional, exact match."`
	Path   string `json:"path,omitempty" jsonschema_description:"Path filter (LIKE substring match). Optional."`
	Limit  int    `json:"limit,omitempty" jsonschema_description:"Max clusters to return (default 50)"`
}

type FindAnomaliesArgs struct {
	Host   string `json:"host" jsonschema:"required" jsonschema_description:"Hostname (LIKE substring match)"`
	Method string `json:"method,omitempty" jsonschema_description:"HTTP method filter (e.g. GET). Optional, exact match."`
	Path   string `json:"path,omitempty" jsonschema_description:"Path filter (LIKE substring match). Optional."`
	Limit  int    `json:"limit,omitempty" jsonschema_description:"Max anomalous rows to return (default 25)"`
}

type clusterRow struct {
	Fingerprint string   `json:"fingerprint"`
	Count       int      `json:"count"`
	Status      int      `json:"status"`
	Mime        string   `json:"mime"`
	LengthBkt   int      `json:"lengthBucket"`
	SampleIDs   []string `json:"sampleIds"`
	Examples    []string `json:"examples"`
}

type anomalyRow struct {
	ID          string `json:"id"`
	Method      string `json:"method"`
	Path        string `json:"path"`
	Fingerprint string `json:"fingerprint"`
	Status      int    `json:"status"`
	Mime        string `json:"mime"`
}

func (backend *Backend) clusterResponsesHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ClusterResponsesArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	limit := args.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}

	if backend.DB == nil {
		return mcp.NewToolResultError("backend database not initialized"), nil
	}

	where, whereArgs := buildClusterWhere(args.Host, args.Method, args.Path, true)

	// Group by fingerprint, return cluster summaries with up to 5 sample IDs
	// and 3 example "method path" strings each.
	q := `
		SELECT
			fingerprint,
			COUNT(*)                                    AS cnt,
			COALESCE(json_extract(resp_json,'$.status'), 0) AS status,
			COALESCE(json_extract(resp_json,'$.mime'),  '') AS mime,
			GROUP_CONCAT(id, '|')                       AS ids,
			GROUP_CONCAT(
				COALESCE(json_extract(req_json,'$.method'),'') || ' ' ||
				COALESCE(json_extract(req_json,'$.path'),''),
				'|'
			)                                           AS examples
		FROM _data
		` + where + `
		  AND fingerprint != ''
		  AND has_resp = TRUE
		GROUP BY fingerprint
		ORDER BY cnt DESC
		LIMIT ?`

	whereArgs = append(whereArgs, limit)

	rows, err := backend.DB.Query(q, whereArgs...)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("query failed: %v", err)), nil
	}
	defer rows.Close()

	clusters := make([]clusterRow, 0, limit)
	totalRows := 0
	for rows.Next() {
		var c clusterRow
		var ids, examples sql.NullString
		var lengthBkt int
		if err := rows.Scan(&c.Fingerprint, &c.Count, &c.Status, &c.Mime, &ids, &examples); err != nil {
			continue
		}
		c.LengthBkt = lengthBkt // populated from fingerprint string below
		if i := strings.Index(c.Fingerprint, "-l"); i >= 0 {
			fmt.Sscanf(c.Fingerprint[i+2:], "%d", &c.LengthBkt)
		}
		c.SampleIDs = topNSplit(ids.String, "|", 5)
		c.Examples = topNSplit(examples.String, "|", 3)
		clusters = append(clusters, c)
		totalRows += c.Count
	}

	return mcpJSONResult(map[string]any{
		"clusters":      clusters,
		"clusterCount":  len(clusters),
		"totalResponses": totalRows,
		"filter": map[string]any{
			"host":   args.Host,
			"method": args.Method,
			"path":   args.Path,
		},
	})
}

func (backend *Backend) findAnomaliesHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args FindAnomaliesArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if strings.TrimSpace(args.Host) == "" {
		return mcp.NewToolResultError("host is required"), nil
	}

	limit := args.Limit
	if limit <= 0 || limit > 200 {
		limit = 25
	}

	if backend.DB == nil {
		return mcp.NewToolResultError("backend database not initialized"), nil
	}

	where, whereArgs := buildClusterWhere(args.Host, args.Method, args.Path, true)

	// Step 1: find the modal fingerprint for this scope.
	modalQ := `
		SELECT fingerprint, COUNT(*) AS cnt
		FROM _data
		` + where + `
		  AND fingerprint != ''
		  AND has_resp = TRUE
		GROUP BY fingerprint
		ORDER BY cnt DESC
		LIMIT 1`

	var modalFP string
	var modalCount int
	if err := backend.DB.QueryRow(modalQ, whereArgs...).Scan(&modalFP, &modalCount); err != nil {
		if err == sql.ErrNoRows {
			return mcpJSONResult(map[string]any{
				"modal":     nil,
				"anomalies": []anomalyRow{},
				"note":      "no fingerprinted responses found for this scope",
			})
		}
		return mcp.NewToolResultError(fmt.Sprintf("modal query failed: %v", err)), nil
	}

	// Step 2: list responses whose fingerprint differs from the modal one.
	anomalyQ := `
		SELECT
			id,
			COALESCE(json_extract(req_json,'$.method'),'') AS method,
			COALESCE(json_extract(req_json,'$.path'),'')   AS path,
			fingerprint,
			COALESCE(json_extract(resp_json,'$.status'),0) AS status,
			COALESCE(json_extract(resp_json,'$.mime'), '') AS mime
		FROM _data
		` + where + `
		  AND fingerprint != ''
		  AND fingerprint != ?
		  AND has_resp = TRUE
		ORDER BY "index" DESC
		LIMIT ?`

	allArgs := append(append([]any{}, whereArgs...), modalFP, limit)

	rows, err := backend.DB.Query(anomalyQ, allArgs...)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("anomaly query failed: %v", err)), nil
	}
	defer rows.Close()

	anomalies := make([]anomalyRow, 0, limit)
	for rows.Next() {
		var a anomalyRow
		if err := rows.Scan(&a.ID, &a.Method, &a.Path, &a.Fingerprint, &a.Status, &a.Mime); err != nil {
			continue
		}
		anomalies = append(anomalies, a)
	}

	return mcpJSONResult(map[string]any{
		"modal": map[string]any{
			"fingerprint": modalFP,
			"count":       modalCount,
		},
		"anomalies":     anomalies,
		"anomalyCount":  len(anomalies),
		"filter": map[string]any{
			"host":   args.Host,
			"method": args.Method,
			"path":   args.Path,
		},
	})
}

// buildClusterWhere assembles a WHERE clause from optional filters. requireOne
// guarantees at least one condition (defaulting to "1=1") so the caller can
// always concatenate "AND ..." after it.
func buildClusterWhere(host, method, path string, requireOne bool) (string, []any) {
	var conds []string
	var args []any

	if h := strings.TrimSpace(host); h != "" {
		conds = append(conds, "host LIKE ?")
		args = append(args, "%"+h+"%")
	}
	if m := strings.TrimSpace(method); m != "" {
		conds = append(conds, "json_extract(req_json,'$.method') = ?")
		args = append(args, strings.ToUpper(m))
	}
	if p := strings.TrimSpace(path); p != "" {
		conds = append(conds, "json_extract(req_json,'$.path') LIKE ?")
		args = append(args, "%"+p+"%")
	}

	if len(conds) == 0 {
		if requireOne {
			return "WHERE 1=1", args
		}
		return "", args
	}
	return "WHERE " + strings.Join(conds, " AND "), args
}

// topNSplit splits s by sep and returns the first n parts. Empty input
// returns nil so JSON serializes as null/[] rather than [""].
func topNSplit(s, sep string, n int) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, sep)
	if len(parts) > n {
		parts = parts[:n]
	}
	return parts
}
