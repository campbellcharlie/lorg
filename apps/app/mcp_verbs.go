package app

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// Higher-order MCP verbs.
//
// These tools fold what would otherwise be 5–20 primitive tool calls into a
// single bounded SQL summary. They do NOT issue new HTTP traffic — they
// derive structure from what's already been captured. The agent can then
// decide which leads to probe further with sendRequest, raceTest, etc.
//
// Verbs in this file:
//   - mapEndpoints(host): structured endpoint map with response-shape stats
//   - probeAuth(host):    auth boundary surface with probe candidates
// ---------------------------------------------------------------------------

type MapEndpointsArgs struct {
	Host  string `json:"host" jsonschema:"required" jsonschema_description:"Target hostname (LIKE substring match)"`
	Limit int    `json:"limit,omitempty" jsonschema_description:"Max endpoints to return (default 100)"`
}

type ProbeAuthArgs struct {
	Host  string `json:"host" jsonschema:"required" jsonschema_description:"Target hostname (LIKE substring match)"`
	Limit int    `json:"limit,omitempty" jsonschema_description:"Max probe candidates to return (default 25)"`
}

type endpointSummary struct {
	Method            string   `json:"method"`
	PathTemplate      string   `json:"pathTemplate"`
	ConcreteSamples   []string `json:"concreteSamples"`
	Count             int      `json:"count"`
	DistinctFps       int      `json:"distinctFingerprints"`
	ModalFingerprint  string   `json:"modalFingerprint,omitempty"`
	StatusDistribution map[int]int `json:"statusDistribution"`
	HasParams         bool     `json:"hasParams"`
}

type authEndpoint struct {
	Method        string `json:"method"`
	Path          string `json:"path"`
	AuthMechanism string `json:"authMechanism"`
	LastStatus    int    `json:"lastStatus"`
	SampleID      string `json:"sampleId"`
}

type denialBucket struct {
	Status int    `json:"status"`
	Path   string `json:"path"`
	Method string `json:"method"`
	Count  int    `json:"count"`
}

func (backend *Backend) mapEndpointsHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args MapEndpointsArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if strings.TrimSpace(args.Host) == "" {
		return mcp.NewToolResultError("host is required"), nil
	}
	limit := args.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	if backend.DB == nil {
		return mcp.NewToolResultError("backend database not initialized"), nil
	}

	hostPattern := "%" + strings.TrimSpace(args.Host) + "%"

	q := `
		SELECT
			COALESCE(json_extract(req_json,'$.method'),'') AS method,
			COALESCE(json_extract(req_json,'$.path'),  '') AS path,
			COALESCE(json_extract(resp_json,'$.status'),0) AS status,
			fingerprint,
			has_params
		FROM _data
		WHERE host LIKE ?
		  AND has_resp = TRUE
		ORDER BY "index" DESC`

	rows, err := backend.DB.Query(q, hostPattern)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("query failed: %v", err)), nil
	}
	defer rows.Close()

	type aggKey struct{ method, template string }
	type agg struct {
		count     int
		samples   map[string]struct{}
		fps       map[string]int
		statuses  map[int]int
		hasParams bool
	}
	buckets := make(map[aggKey]*agg)

	for rows.Next() {
		var method, path, fp string
		var status int
		var hasParams bool
		if err := rows.Scan(&method, &path, &status, &fp, &hasParams); err != nil {
			continue
		}
		if method == "" || path == "" {
			continue
		}
		template := normalizePathTemplate(path)
		key := aggKey{method: strings.ToUpper(method), template: template}
		b := buckets[key]
		if b == nil {
			b = &agg{
				samples:  map[string]struct{}{},
				fps:      map[string]int{},
				statuses: map[int]int{},
			}
			buckets[key] = b
		}
		b.count++
		if len(b.samples) < 3 {
			b.samples[path] = struct{}{}
		}
		if fp != "" {
			b.fps[fp]++
		}
		if status != 0 {
			b.statuses[status]++
		}
		if hasParams {
			b.hasParams = true
		}
	}

	out := make([]endpointSummary, 0, len(buckets))
	for k, b := range buckets {
		modalFP := ""
		modalCount := 0
		for fp, n := range b.fps {
			if n > modalCount {
				modalCount = n
				modalFP = fp
			}
		}
		samples := make([]string, 0, len(b.samples))
		for s := range b.samples {
			samples = append(samples, s)
		}
		sort.Strings(samples)
		out = append(out, endpointSummary{
			Method:             k.method,
			PathTemplate:       k.template,
			ConcreteSamples:    samples,
			Count:              b.count,
			DistinctFps:        len(b.fps),
			ModalFingerprint:   modalFP,
			StatusDistribution: b.statuses,
			HasParams:          b.hasParams,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	if len(out) > limit {
		out = out[:limit]
	}

	return mcpJSONResult(map[string]any{
		"host":          args.Host,
		"endpoints":     out,
		"endpointCount": len(out),
	})
}

func (backend *Backend) probeAuthHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ProbeAuthArgs
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

	hostPattern := "%" + strings.TrimSpace(args.Host) + "%"

	// 1. Find requests that carried an auth-bearing credential. We pull the
	//    raw request and inspect headers since req_json doesn't always store
	//    a normalized headers map.
	authQ := `
		SELECT id,
		       COALESCE(json_extract(req_json,'$.method'),'') AS method,
		       COALESCE(json_extract(req_json,'$.path'),  '') AS path,
		       COALESCE(json_extract(resp_json,'$.status'),0) AS status,
		       req
		FROM _data
		WHERE host LIKE ?
		  AND has_resp = TRUE
		ORDER BY "index" DESC
		LIMIT 2000`

	rows, err := backend.DB.Query(authQ, hostPattern)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("auth scan failed: %v", err)), nil
	}
	defer rows.Close()

	type endpointKey struct{ method, path string }
	authByEndpoint := map[endpointKey]authEndpoint{}

	for rows.Next() {
		var id, method, path, raw string
		var status int
		if err := rows.Scan(&id, &method, &path, &status, &raw); err != nil {
			continue
		}
		mech := detectAuthMechanism(raw)
		if mech == "" {
			continue
		}
		key := endpointKey{method: strings.ToUpper(method), path: path}
		if _, seen := authByEndpoint[key]; seen {
			continue
		}
		authByEndpoint[key] = authEndpoint{
			Method:        key.method,
			Path:          key.path,
			AuthMechanism: mech,
			LastStatus:    status,
			SampleID:      id,
		}
	}

	authList := make([]authEndpoint, 0, len(authByEndpoint))
	for _, v := range authByEndpoint {
		authList = append(authList, v)
	}
	sort.Slice(authList, func(i, j int) bool {
		if authList[i].Path != authList[j].Path {
			return authList[i].Path < authList[j].Path
		}
		return authList[i].Method < authList[j].Method
	})

	// 2. Find denial responses (401 / 403) — these mark known auth boundaries
	//    even when our request capture didn't include the credential.
	denialQ := `
		SELECT
			COALESCE(json_extract(resp_json,'$.status'),0) AS status,
			COALESCE(json_extract(req_json,'$.method'),'') AS method,
			COALESCE(json_extract(req_json,'$.path'),  '') AS path,
			COUNT(*) AS cnt
		FROM _data
		WHERE host LIKE ?
		  AND has_resp = TRUE
		  AND json_extract(resp_json,'$.status') IN (401, 403)
		GROUP BY status, method, path
		ORDER BY cnt DESC
		LIMIT ?`

	drows, err := backend.DB.Query(denialQ, hostPattern, limit)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("denial scan failed: %v", err)), nil
	}
	defer drows.Close()

	denials := make([]denialBucket, 0)
	for drows.Next() {
		var d denialBucket
		if err := drows.Scan(&d.Status, &d.Method, &d.Path, &d.Count); err != nil {
			continue
		}
		denials = append(denials, d)
	}

	// 3. Probe candidates: endpoints that DID succeed with auth — sending
	//    them again without the credential is the obvious next move for
	//    an authz check. Cap to limit.
	candidates := make([]string, 0, len(authList))
	for _, a := range authList {
		if a.LastStatus >= 200 && a.LastStatus < 300 {
			candidates = append(candidates, a.Method+" "+a.Path)
			if len(candidates) >= limit {
				break
			}
		}
	}

	return mcpJSONResult(map[string]any{
		"host":            args.Host,
		"authEndpoints":   authList,
		"authCount":       len(authList),
		"denials":         denials,
		"denialCount":     len(denials),
		"probeCandidates": candidates,
		"hint":            "Replay each probeCandidate with sendRequest using a session that has NO cookies/auth header. Compare response fingerprints with findAnomalies — non-modal results are likely access-control bugs.",
	})
}

// normalizePathTemplate collapses common dynamic segments (numeric IDs, UUIDs,
// hex digests) into placeholders so /users/123 and /users/456 collapse to
// /users/{id}. Conservative: only obvious cases. Avoids over-aggressive
// templating that would lose useful endpoint detail.
func normalizePathTemplate(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if p == "" {
			continue
		}
		switch {
		case isAllDigits(p):
			parts[i] = "{id}"
		case isUUID(p):
			parts[i] = "{uuid}"
		case isHexBlob(p):
			parts[i] = "{hex}"
		}
	}
	return strings.Join(parts, "/")
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isUUID(s string) bool { return uuidRe.MatchString(s) }

// isHexBlob matches >=16 char pure-hex strings (e.g. SHA digests, ETags,
// session IDs). Shorter strings stay literal so we don't collapse genuine
// path words like "abc" or "feed".
func isHexBlob(s string) bool {
	if len(s) < 16 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

// detectAuthMechanism looks at the headers section of a raw HTTP request and
// returns a short label for the credential it carries, or "" if none found.
// Order of preference reflects what's most informative for an authz probe.
func detectAuthMechanism(raw string) string {
	if raw == "" {
		return ""
	}
	headers, _ := splitHTTPRaw(raw)
	lower := strings.ToLower(headers)

	switch {
	case strings.Contains(lower, "\nauthorization: bearer "):
		return "Bearer"
	case strings.Contains(lower, "\nauthorization: basic "):
		return "Basic"
	case strings.Contains(lower, "\nauthorization: "):
		return "Authorization"
	case strings.Contains(lower, "\nx-api-key:") || strings.Contains(lower, "\napi-key:"):
		return "APIKey"
	case strings.Contains(lower, "\ncookie:"):
		return "Cookie"
	case strings.Contains(lower, "\nx-auth-token:"):
		return "AuthToken"
	}
	return ""
}

// silence unused-import warning in some build configurations where database/sql
// isn't directly referenced in this file.
var _ = sql.ErrNoRows
