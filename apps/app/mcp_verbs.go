package app

import (
	"context"
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
	Host    string `json:"host" jsonschema:"required" jsonschema_description:"Target hostname (LIKE substring match)"`
	Project string `json:"project,omitempty" jsonschema_description:"Optional project tag filter. Independent from the UI's currently-viewed project — the agent can scope reads to any captured project."`
	Limit   int    `json:"limit,omitempty" jsonschema_description:"Max endpoints to return (default 100)"`
}

type ProbeAuthArgs struct {
	Host    string `json:"host" jsonschema:"required" jsonschema_description:"Target hostname (LIKE substring match)"`
	Project string `json:"project,omitempty" jsonschema_description:"Optional project tag filter. Independent from the UI's currently-viewed project — the agent can scope reads to any captured project."`
	Limit   int    `json:"limit,omitempty" jsonschema_description:"Max probe candidates to return (default 25)"`
}

type endpointSummary struct {
	Method             string      `json:"method"`
	PathTemplate       string      `json:"pathTemplate"`
	ConcreteSamples    []string    `json:"concreteSamples"`
	Count              int         `json:"count"`
	DistinctFps        int         `json:"distinctFingerprints"`
	ModalFingerprint   string      `json:"modalFingerprint,omitempty"`
	StatusDistribution map[int]int `json:"statusDistribution"`
	HasParams          bool        `json:"hasParams"`
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

	// Read across all project DBs via the union layer (ADR-004 E8); has_resp is
	// approximated by status != 0, has_params by a non-empty query string.
	urows, err := projectDB.unionTrafficRows(args.Project, "host LIKE ?",
		[]any{"%" + strings.TrimSpace(args.Host) + "%"}, 0)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("query failed: %v", err)), nil
	}

	type aggKey struct{ method, template string }
	type agg struct {
		count     int
		samples   map[string]struct{}
		fps       map[string]int
		statuses  map[int]int
		hasParams bool
	}
	buckets := make(map[aggKey]*agg)

	for _, r := range urows {
		method, path, fp := r.Method, r.Path, r.Fingerprint
		status := r.Status
		hasParams := r.Query != ""
		if status == 0 { // has_resp filter
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

	// Read across all project DBs via the union layer (ADR-004 E8), capped.
	urows, err := projectDB.unionTrafficRows(args.Project, "host LIKE ?",
		[]any{"%" + strings.TrimSpace(args.Host) + "%"}, 2000)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("auth scan failed: %v", err)), nil
	}

	// 1. Requests that carried an auth-bearing credential — reconstruct each raw
	//    request from http_messages and inspect its headers.
	type endpointKey struct{ method, path string }
	authByEndpoint := map[endpointKey]authEndpoint{}
	for _, r := range urows {
		if r.Status == 0 { // has_resp filter
			continue
		}
		key := endpointKey{method: strings.ToUpper(r.Method), path: r.Path}
		if _, seen := authByEndpoint[key]; seen {
			continue
		}
		rawReq, _, berr := projectDB.getTrafficBytes(r.Project, r.RequestID)
		if berr != nil {
			continue
		}
		mech := detectAuthMechanism(rawReq)
		if mech == "" {
			continue
		}
		authByEndpoint[key] = authEndpoint{
			Method:        key.method,
			Path:          key.path,
			AuthMechanism: mech,
			LastStatus:    r.Status,
			SampleID:      makeRowID(r.Project, r.RequestID),
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

	// 2. Denial responses (401 / 403) from the same rows — known auth boundaries.
	type dkey struct {
		status       int
		method, path string
	}
	dcounts := map[dkey]int{}
	for _, r := range urows {
		if r.Status == 401 || r.Status == 403 {
			dcounts[dkey{r.Status, r.Method, r.Path}]++
		}
	}
	denials := make([]denialBucket, 0, len(dcounts))
	for k, cnt := range dcounts {
		denials = append(denials, denialBucket{Status: k.status, Method: k.method, Path: k.path, Count: cnt})
	}
	sort.Slice(denials, func(i, j int) bool { return denials[i].Count > denials[j].Count })
	if len(denials) > limit {
		denials = denials[:limit]
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
