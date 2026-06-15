package app

import (
	"context"
	"fmt"
	"sort"
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
	Host    string `json:"host,omitempty" jsonschema_description:"Hostname filter (LIKE substring match). Optional."`
	Method  string `json:"method,omitempty" jsonschema_description:"HTTP method filter (e.g. GET, POST). Optional, exact match."`
	Path    string `json:"path,omitempty" jsonschema_description:"Path filter (LIKE substring match). Optional."`
	Project string `json:"project,omitempty" jsonschema_description:"Optional project tag filter. Independent from the UI's currently-viewed project."`
	Limit   int    `json:"limit,omitempty" jsonschema_description:"Max clusters to return (default 50)"`
}

type FindAnomaliesArgs struct {
	Host    string `json:"host" jsonschema:"required" jsonschema_description:"Hostname (LIKE substring match)"`
	Method  string `json:"method,omitempty" jsonschema_description:"HTTP method filter (e.g. GET). Optional, exact match."`
	Path    string `json:"path,omitempty" jsonschema_description:"Path filter (LIKE substring match). Optional."`
	Project string `json:"project,omitempty" jsonschema_description:"Optional project tag filter. Independent from the UI's currently-viewed project."`
	Limit   int    `json:"limit,omitempty" jsonschema_description:"Max anomalous rows to return (default 25)"`
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

	// Group across the cross-project union of http_traffic by fingerprint
	// (ADR-004 E5). fingerprint is now a real http_traffic column (E1).
	where, whereArgs := clusterWhereHT(args.Host, args.Method, args.Path)
	rows, err := projectDB.unionTrafficRows(args.Project, where, whereArgs, 0)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("query failed: %v", err)), nil
	}

	clusters, totalRows := clusterByFingerprint(rows, limit)

	return mcpJSONResult(map[string]any{
		"clusters":       clusters,
		"clusterCount":   len(clusters),
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

	where, whereArgs := clusterWhereHT(args.Host, args.Method, args.Path)
	rows, err := projectDB.unionTrafficRows(args.Project, where, whereArgs, 0)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("modal query failed: %v", err)), nil
	}

	modalFP, modalCount, anomalies := anomaliesFromRows(rows, limit)
	if modalCount == 0 {
		return mcpJSONResult(map[string]any{
			"modal":     nil,
			"anomalies": []anomalyRow{},
			"note":      "no fingerprinted responses found for this scope",
		})
	}

	return mcpJSONResult(map[string]any{
		"modal": map[string]any{
			"fingerprint": modalFP,
			"count":       modalCount,
		},
		"anomalies":    anomalies,
		"anomalyCount": len(anomalies),
		"filter": map[string]any{
			"host":   args.Host,
			"method": args.Method,
			"path":   args.Path,
		},
	})
}

// clusterWhereHT builds a WHERE clause over http_traffic's flat columns for the
// union read layer (ADR-004 E5). Project scoping is handled by unionTrafficRows.
func clusterWhereHT(host, method, path string) (string, []any) {
	var conds []string
	var args []any
	if h := strings.TrimSpace(host); h != "" {
		conds = append(conds, "host LIKE ?")
		args = append(args, "%"+h+"%")
	}
	if m := strings.TrimSpace(method); m != "" {
		conds = append(conds, "method = ?")
		args = append(args, strings.ToUpper(m))
	}
	if p := strings.TrimSpace(path); p != "" {
		conds = append(conds, "path LIKE ?")
		args = append(args, "%"+p+"%")
	}
	conds = append(conds, "fingerprint != ''")
	return strings.Join(conds, " AND "), args
}

// clusterByFingerprint groups union rows by fingerprint into cluster summaries
// (sorted by count desc, capped at limit) and returns the total grouped rows.
// Pure function over the union output — testable without a DB.
func clusterByFingerprint(rows []TrafficRow, limit int) ([]clusterRow, int) {
	type agg struct {
		count    int
		status   int
		mime     string
		ids      []string
		examples []string
	}
	byFP := make(map[string]*agg)
	order := make([]string, 0)
	total := 0
	for _, r := range rows {
		if r.Fingerprint == "" {
			continue
		}
		a := byFP[r.Fingerprint]
		if a == nil {
			a = &agg{status: r.Status, mime: r.Mime}
			byFP[r.Fingerprint] = a
			order = append(order, r.Fingerprint)
		}
		a.count++
		total++
		if len(a.ids) < 5 {
			a.ids = append(a.ids, makeRowID(r.Project, r.RequestID))
		}
		if len(a.examples) < 3 {
			a.examples = append(a.examples, strings.TrimSpace(r.Method+" "+r.Path))
		}
	}
	clusters := make([]clusterRow, 0, len(order))
	for _, fp := range order {
		a := byFP[fp]
		c := clusterRow{Fingerprint: fp, Count: a.count, Status: a.status, Mime: a.mime, SampleIDs: a.ids, Examples: a.examples}
		if i := strings.Index(fp, "-l"); i >= 0 {
			fmt.Sscanf(fp[i+2:], "%d", &c.LengthBkt)
		}
		clusters = append(clusters, c)
	}
	sort.Slice(clusters, func(i, j int) bool { return clusters[i].Count > clusters[j].Count })
	if len(clusters) > limit {
		clusters = clusters[:limit]
	}
	return clusters, total
}

// anomaliesFromRows finds the modal fingerprint and returns rows that deviate
// from it (capped at limit). modalCount==0 means no fingerprinted rows. Pure
// function over the union output. Rows are assumed already global_seq DESC.
func anomaliesFromRows(rows []TrafficRow, limit int) (modalFP string, modalCount int, anomalies []anomalyRow) {
	counts := make(map[string]int)
	for _, r := range rows {
		if r.Fingerprint != "" {
			counts[r.Fingerprint]++
		}
	}
	if len(counts) == 0 {
		return "", 0, []anomalyRow{}
	}
	for fp, c := range counts {
		if c > modalCount {
			modalFP, modalCount = fp, c
		}
	}
	anomalies = make([]anomalyRow, 0, limit)
	for _, r := range rows {
		if len(anomalies) >= limit {
			break
		}
		if r.Fingerprint == "" || r.Fingerprint == modalFP {
			continue
		}
		anomalies = append(anomalies, anomalyRow{
			ID:          makeRowID(r.Project, r.RequestID),
			Method:      r.Method,
			Path:        r.Path,
			Fingerprint: r.Fingerprint,
			Status:      r.Status,
			Mime:        r.Mime,
		})
	}
	return modalFP, modalCount, anomalies
}

