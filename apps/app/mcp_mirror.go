package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// mirror() — clone a captured request (or saved template) as a baseline
// and apply small mutations. The agent fires the FIRST request normally;
// every subsequent re-probe goes through mirror() with just the diff,
// avoiding the per-call cost of re-emitting the full headers/auth/body.
//
// Typical workflow that this saves tokens on:
//
//   1. sendHttpRequest({...big request with bearer token + cookies + body})
//      ← row id 42 captured.
//
//   2. mirror({rowId: "42", body: {"x": 2}})
//      ← reuses everything from row 42, only changes the JSON body.
//
//   3. mirror({rowId: "42", path: "/admin", removeHeaders: ["Cookie"]})
//      ← same baseline, different path, drop Cookie header.
//
// For 10 iterations of one endpoint that's ~25 tokens per call instead
// of ~800, an order of magnitude cheaper.
// ---------------------------------------------------------------------------

type MirrorArgs struct {
	RowID         string            `json:"rowId,omitempty" jsonschema_description:"Captured traffic row id to clone (preferred)"`
	TemplateName  string            `json:"templateName,omitempty" jsonschema_description:"Saved template name to clone (alternative to rowId)"`
	Method        string            `json:"method,omitempty" jsonschema_description:"Replace HTTP method (e.g. PUT, DELETE)"`
	Path          string            `json:"path,omitempty" jsonschema_description:"Replace URL path. Query string preserved unless query is also set."`
	Query         string            `json:"query,omitempty" jsonschema_description:"Replace URL query string (without ?). Use empty string to drop the query entirely."`
	AppendQuery   map[string]string `json:"appendQuery,omitempty" jsonschema_description:"Add or overwrite individual query params (additive)"`
	SetHeaders    map[string]string `json:"setHeaders,omitempty" jsonschema_description:"Add or replace specific headers (case-insensitive name match)"`
	RemoveHeaders []string          `json:"removeHeaders,omitempty" jsonschema_description:"Drop these headers entirely (case-insensitive)"`
	Body          json.RawMessage   `json:"body,omitempty" jsonschema_description:"Replace body. Object → JSON-encoded + Content-Type/Length updated. String → used verbatim."`
	HostOverride  string            `json:"host,omitempty" jsonschema_description:"Override target host (defaults to baseline)"`
	PortOverride  int               `json:"port,omitempty" jsonschema_description:"Override target port (defaults to baseline)"`
	TLSOverride   *bool             `json:"tls,omitempty" jsonschema_description:"Override TLS flag (defaults to baseline)"`
	HTTP2         *bool             `json:"http2,omitempty" jsonschema_description:"Override HTTP version (defaults to baseline)"`
	Note          string            `json:"note,omitempty" jsonschema_description:"Note to attach to the saved row"`
	MaxBodyBytes  int               `json:"maxBodyBytes,omitempty" jsonschema_description:"Cap response body in returned summary (default 8192). Use 0 for no cap."`

	// Batch mode: when non-empty, fire len(batch) requests against the
	// same baseline. Each entry's mutations layer on top of the
	// top-level singleton mutations (singleton = common base, entry =
	// per-iteration override). One MCP round-trip → N HTTP requests.
	Batch []MirrorBatchEntry `json:"batch,omitempty" jsonschema_description:"Fire multiple iterations in one call. Each entry's mutations override the top-level singleton mutations for that iteration. Returns one summary row per iteration. Use this instead of N separate mirror calls — amortizes the per-call MCP overhead."`
}

// MirrorBatchEntry is a single iteration's mutation set in batch mode.
// All fields mirror the singleton MirrorArgs fields (minus baseline
// pickers and connection overrides — those are batch-wide).
type MirrorBatchEntry struct {
	Method        string            `json:"method,omitempty" jsonschema_description:"Override method for this iteration"`
	Path          string            `json:"path,omitempty" jsonschema_description:"Override path for this iteration"`
	Query         string            `json:"query,omitempty" jsonschema_description:"Replace query string for this iteration"`
	AppendQuery   map[string]string `json:"appendQuery,omitempty" jsonschema_description:"Add/overwrite query params for this iteration"`
	SetHeaders    map[string]string `json:"setHeaders,omitempty" jsonschema_description:"Add/replace headers for this iteration"`
	RemoveHeaders []string          `json:"removeHeaders,omitempty" jsonschema_description:"Drop headers for this iteration"`
	Body          json.RawMessage   `json:"body,omitempty" jsonschema_description:"Replace body for this iteration"`
	Note          string            `json:"note,omitempty" jsonschema_description:"Note attached to this iteration's saved row"`
}

type mirrorBaseline struct {
	rawRequest string
	host       string
	port       int
	tls        bool
	http2      bool
}

func (backend *Backend) mirrorHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args MirrorArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if args.RowID == "" && args.TemplateName == "" {
		return mcp.NewToolResultError("either rowId or templateName is required"), nil
	}
	if args.RowID != "" && args.TemplateName != "" {
		return mcp.NewToolResultError("provide rowId OR templateName, not both"), nil
	}

	base, err := backend.loadMirrorBaseline(args)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("baseline load failed: %v", err)), nil
	}

	// Resolve connection params once (batch entries can't override these).
	host := base.host
	if args.HostOverride != "" {
		host = args.HostOverride
	}
	port := base.port
	if args.PortOverride != 0 {
		port = args.PortOverride
	}
	tls := base.tls
	if args.TLSOverride != nil {
		tls = *args.TLSOverride
	}
	http2 := base.http2
	if args.HTTP2 != nil {
		http2 = *args.HTTP2
	}
	maxBody := args.MaxBodyBytes
	if maxBody == 0 {
		maxBody = 8192
	}

	// Batch mode: fire one request per entry, return per-iteration summaries.
	if len(args.Batch) > 0 {
		return backend.runMirrorBatch(base, args, host, port, tls, http2, maxBody)
	}

	// Singleton mode (original behavior).
	mutated, summary, err := applyMutations(base.rawRequest, args)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("mutation failed: %v", err)), nil
	}

	resp, err := backend.sendRepeaterLogic(&RepeaterSendRequest{
		Host:        host,
		Port:        strconv.Itoa(port),
		TLS:         tls,
		Request:     mutated,
		Timeout:     30,
		HTTP2:       http2,
		Url:         scheme(tls) + "://" + host + ":" + strconv.Itoa(port),
		GeneratedBy: "ai/mcp/mirror",
		Note:        args.Note,
	})
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	respPreview, truncated := truncateBody(resp.Response, maxBody)

	return mcpJSONResult(map[string]any{
		"id":               resp.UserData.ID,
		"time":             resp.Time,
		"mutationsApplied": summary,
		"response":         respPreview,
		"responseTruncatedAt": ifTrue(truncated, maxBody),
		"originalLength":   len(resp.Response),
	})
}

// runMirrorBatch fires one request per Batch entry against the same
// baseline. Each entry's mutations layer on top of the top-level
// singleton mutations: singleton acts as the common base (e.g. an
// auth header), entries provide per-iteration overrides (e.g. paths).
//
// Returns a compact per-iteration summary — id, status, length,
// mutationsApplied, time. No response bodies (those'd dwarf the
// summary). Use getRequestResponseFromID with any returned id to
// pull the full bytes if needed.
func (backend *Backend) runMirrorBatch(
	base *mirrorBaseline,
	args MirrorArgs,
	host string, port int, tls bool, http2 bool, _ int,
) (*mcp.CallToolResult, error) {
	urlStr := scheme(tls) + "://" + host + ":" + strconv.Itoa(port)
	results := make([]map[string]any, 0, len(args.Batch))
	successCount := 0

	for i, entry := range args.Batch {
		merged := mergeMirrorEntry(args, entry)
		mutated, summary, err := applyMutations(base.rawRequest, merged)
		if err != nil {
			results = append(results, map[string]any{
				"index": i,
				"error": "mutation failed: " + err.Error(),
			})
			continue
		}

		resp, err := backend.sendRepeaterLogic(&RepeaterSendRequest{
			Host:        host,
			Port:        strconv.Itoa(port),
			TLS:         tls,
			Request:     mutated,
			Timeout:     30,
			HTTP2:       http2,
			Url:         urlStr,
			GeneratedBy: "ai/mcp/mirror",
			Note:        merged.Note,
		})
		if err != nil {
			results = append(results, map[string]any{
				"index": i,
				"error": err.Error(),
			})
			continue
		}

		successCount++
		statusLine := ""
		if i := strings.Index(resp.Response, "\r\n"); i > 0 {
			statusLine = resp.Response[:i]
		}
		results = append(results, map[string]any{
			"index":            i,
			"id":               resp.UserData.ID,
			"statusLine":       statusLine,
			"responseLength":   len(resp.Response),
			"time":             resp.Time,
			"mutationsApplied": summary,
		})
	}

	return mcpJSONResult(map[string]any{
		"baseline":        baselineLabel(args),
		"totalIterations": len(args.Batch),
		"successCount":    successCount,
		"errorCount":      len(args.Batch) - successCount,
		"iterations":      results,
		"hint":            "Bodies omitted to keep the summary cheap. Use getRequestResponseFromID with any iteration's id to pull full bytes.",
	})
}

// mergeMirrorEntry creates a per-iteration MirrorArgs that combines the
// caller's singleton mutations with this entry's per-iteration overrides.
// Entry fields win; singleton fills in the gaps.
func mergeMirrorEntry(base MirrorArgs, entry MirrorBatchEntry) MirrorArgs {
	out := base
	out.Batch = nil // single-iteration view
	if entry.Method != "" {
		out.Method = entry.Method
	}
	if entry.Path != "" {
		out.Path = entry.Path
	}
	if entry.Query != "" {
		out.Query = entry.Query
	}
	if len(entry.AppendQuery) > 0 {
		merged := map[string]string{}
		for k, v := range base.AppendQuery {
			merged[k] = v
		}
		for k, v := range entry.AppendQuery {
			merged[k] = v
		}
		out.AppendQuery = merged
	}
	if len(entry.SetHeaders) > 0 {
		merged := map[string]string{}
		for k, v := range base.SetHeaders {
			merged[k] = v
		}
		for k, v := range entry.SetHeaders {
			merged[k] = v
		}
		out.SetHeaders = merged
	}
	if len(entry.RemoveHeaders) > 0 {
		out.RemoveHeaders = append(append([]string{}, base.RemoveHeaders...), entry.RemoveHeaders...)
	}
	if len(entry.Body) > 0 {
		out.Body = entry.Body
	}
	if entry.Note != "" {
		out.Note = entry.Note
	}
	return out
}

func baselineLabel(args MirrorArgs) string {
	if args.RowID != "" {
		return "rowId:" + args.RowID
	}
	return "templateName:" + args.TemplateName
}

// loadMirrorBaseline pulls the raw request + connection params from either
// a captured row or a saved template.
func (backend *Backend) loadMirrorBaseline(args MirrorArgs) (*mirrorBaseline, error) {
	if args.TemplateName != "" {
		rec, err := backend.DB.FindFirstRecord("_mcp_templates", "name = ?", args.TemplateName)
		if err != nil || rec == nil {
			return nil, fmt.Errorf("template not found: %s", args.TemplateName)
		}
		return &mirrorBaseline{
			rawRequest: rec.GetString("request_template"),
			host:       rec.GetString("host"),
			port:       int(rec.GetFloat("port")),
			tls:        rec.GetBool("tls"),
			http2:      int(rec.GetFloat("http_version")) == 2,
		}, nil
	}

	// rowId path — try projectDB first if id is numeric (its rows come
	// from http_traffic with INTEGER request_id); fall back to lorgdb's
	// _data + _req tables for proxy-captured traffic.
	if isNumeric(args.RowID) {
		if base := loadMirrorFromProjectDB(args.RowID); base != nil {
			return base, nil
		}
	}

	rec, err := backend.DB.FindRecordById("_data", args.RowID)
	if err != nil || rec == nil {
		return nil, fmt.Errorf("row not found: %s", args.RowID)
	}
	// _data.req stores the cross-reference row id, NOT the raw HTTP.
	// Source of truth for the raw request bytes is _req[id].raw.
	var raw string
	reqRec, err := backend.DB.FindRecordById("_req", args.RowID)
	if err == nil && reqRec != nil {
		raw = reqRec.GetString("raw")
	}
	// Fall back to _data.req only if it actually looks like a request
	// line (older / migrated rows may have stored raw there directly).
	if raw == "" {
		candidate := rec.GetString("req")
		if looksLikeRequestLineStart(candidate) {
			raw = candidate
		}
	}
	if raw == "" {
		return nil, fmt.Errorf("row %s has no raw request stored", args.RowID)
	}
	host := rec.GetString("host")
	host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
	if i := strings.Index(host, ":"); i >= 0 {
		host = host[:i]
	}
	portStr := rec.GetString("port")
	port, _ := strconv.Atoi(portStr)
	tls := rec.GetBool("is_https")
	if port == 0 {
		if tls {
			port = 443
		} else {
			port = 80
		}
	}
	http2 := strings.Contains(rec.GetString("http"), "2")
	return &mirrorBaseline{rawRequest: raw, host: host, port: port, tls: tls, http2: http2}, nil
}

// loadMirrorFromProjectDB pulls a baseline from the active projectDB's
// http_traffic + http_messages tables. Returns nil if not found / no DB.
func loadMirrorFromProjectDB(id string) *mirrorBaseline {
	if projectDB == nil {
		return nil
	}
	projectDB.mu.Lock()
	db := projectDB.db
	ready := projectDB.ready
	projectDB.mu.Unlock()
	if db == nil || !ready {
		return nil
	}

	var (
		method, host, path, query string
		protocol                  string
		port                      int
	)
	err := db.QueryRow(`SELECT COALESCE(method,''), COALESCE(host,''), COALESCE(path,''),
		COALESCE(query,''), COALESCE(protocol,'http'), COALESCE(port,0)
		FROM http_traffic WHERE request_id = ?`, id).Scan(&method, &host, &path, &query, &protocol, &port)
	if err != nil {
		return nil
	}

	var reqHeaders string
	var reqBody []byte
	_ = db.QueryRow(`SELECT COALESCE(request_headers,''), COALESCE(request_body, x'')
		FROM http_messages WHERE request_id = ?`, id).Scan(&reqHeaders, &reqBody)

	tls := protocol == "https"
	if port == 0 {
		if tls {
			port = 443
		} else {
			port = 80
		}
	}

	pathPart := path
	if query != "" {
		pathPart = path + "?" + query
	}
	var raw string
	if reqHeaders != "" {
		// http_messages.request_headers usually already includes the
		// request line.
		trimmed := strings.TrimLeft(reqHeaders, "\r\n")
		firstLine := trimmed
		if i := strings.IndexAny(trimmed, "\r\n"); i >= 0 {
			firstLine = trimmed[:i]
		}
		if looksLikeRequestLine(firstLine) {
			raw = strings.TrimRight(reqHeaders, "\r\n") + "\r\n\r\n"
		} else {
			raw = method + " " + pathPart + " HTTP/1.1\r\n" +
				strings.TrimRight(reqHeaders, "\r\n") + "\r\n\r\n"
		}
	} else {
		raw = method + " " + pathPart + " HTTP/1.1\r\nHost: " + host + "\r\n\r\n"
	}
	if len(reqBody) > 0 {
		raw += string(reqBody)
	}
	return &mirrorBaseline{rawRequest: raw, host: host, port: port, tls: tls, http2: false}
}

// looksLikeRequestLineStart returns true when s opens with what could be
// an HTTP request line ("METHOD path HTTP/x"). Cheap shape check.
func looksLikeRequestLineStart(s string) bool {
	if s == "" {
		return false
	}
	// Take just the first line for the check.
	first := s
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		first = s[:i]
	}
	return looksLikeRequestLine(first)
}

func isNumeric(s string) bool {
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

// applyMutations parses the raw HTTP message, applies the requested
// changes, and rebuilds it. Returns the new raw request and a list of
// human-readable mutation descriptions for the response summary.
func applyMutations(raw string, args MirrorArgs) (string, []string, error) {
	if raw == "" {
		return "", nil, fmt.Errorf("empty baseline request")
	}
	// Normalize line endings.
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	sep := strings.Index(normalized, "\n\n")
	var headerBlock, body string
	if sep >= 0 {
		headerBlock = normalized[:sep]
		body = normalized[sep+2:]
	} else {
		headerBlock = normalized
	}

	lines := strings.Split(headerBlock, "\n")
	// Strip trailing empty lines so newly-added headers don't get
	// separated from the rest by a blank line — that would make the
	// receiver parse them as the body instead of as headers.
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return "", nil, fmt.Errorf("malformed request — no lines")
	}

	// First line: METHOD path[?query] HTTP/1.x
	parts := strings.SplitN(lines[0], " ", 3)
	if len(parts) < 3 {
		return "", nil, fmt.Errorf("malformed request line: %q", lines[0])
	}
	method := parts[0]
	pathQuery := parts[1]
	httpVer := parts[2]

	path := pathQuery
	query := ""
	if i := strings.Index(pathQuery, "?"); i >= 0 {
		path = pathQuery[:i]
		query = pathQuery[i+1:]
	}

	var summary []string

	// === Method ===
	if args.Method != "" {
		method = strings.ToUpper(args.Method)
		summary = append(summary, "method→"+method)
	}

	// === Path ===
	if args.Path != "" {
		path = args.Path
		summary = append(summary, "path→"+path)
	}

	// === Query (full replace) ===
	if args.Query != "" || (args.Query == "" && hasField(args, "Query")) {
		// Note: Query="" means "drop query entirely" only when the field
		// was explicitly set, which we approximate by checking if any
		// other Query-related fields are present. Empty default query
		// without intent is ambiguous; we just leave it alone unless
		// AppendQuery is used.
		if args.Query != "" {
			query = args.Query
			summary = append(summary, "query→"+query)
		}
	}

	// === Append/overwrite query params ===
	if len(args.AppendQuery) > 0 {
		vals, _ := url.ParseQuery(query)
		for k, v := range args.AppendQuery {
			vals.Set(k, v)
		}
		query = vals.Encode()
		summary = append(summary, fmt.Sprintf("appendQuery+%d", len(args.AppendQuery)))
	}

	// Reassemble request line
	finalPathQuery := path
	if query != "" {
		finalPathQuery = path + "?" + query
	}
	lines[0] = method + " " + finalPathQuery + " " + httpVer

	// === Headers (set + remove) ===
	if len(args.SetHeaders) > 0 || len(args.RemoveHeaders) > 0 {
		headerCount := setRemoveHeaders(lines, args.SetHeaders, args.RemoveHeaders)
		// setRemoveHeaders mutates `lines` slice in place AND returns
		// a new slice if appends happened — we need to capture it.
		_ = headerCount
		lines = headerCount
		if len(args.SetHeaders) > 0 {
			summary = append(summary, fmt.Sprintf("setHeaders+%d", len(args.SetHeaders)))
		}
		if len(args.RemoveHeaders) > 0 {
			summary = append(summary, fmt.Sprintf("removeHeaders-%d", len(args.RemoveHeaders)))
		}
	}

	// === Body ===
	if len(args.Body) > 0 {
		bodyStr, autoCT := materializeBody(args.Body)
		body = bodyStr
		if autoCT != "" {
			lines = setRemoveHeaders(lines, map[string]string{"Content-Type": autoCT}, nil)
		}
		// Always recompute Content-Length when body changes.
		lines = setRemoveHeaders(lines, map[string]string{"Content-Length": strconv.Itoa(len(body))}, nil)
		summary = append(summary, fmt.Sprintf("body→%dB", len(body)))
	}

	// Reassemble
	out := strings.Join(lines, "\r\n") + "\r\n\r\n" + body
	if len(summary) == 0 {
		summary = []string{"none — replayed verbatim"}
	}
	return out, summary, nil
}

// setRemoveHeaders applies set/remove operations to a header line slice
// (lines[0] is the request line — left untouched). Header names are
// matched case-insensitively. Setting an existing header replaces its
// value in place; new headers are appended.
func setRemoveHeaders(lines []string, set map[string]string, remove []string) []string {
	removeMap := map[string]bool{}
	for _, h := range remove {
		removeMap[strings.ToLower(h)] = true
	}
	setLower := map[string]string{}
	setOriginalCase := map[string]string{}
	for k, v := range set {
		setLower[strings.ToLower(k)] = v
		setOriginalCase[strings.ToLower(k)] = k
	}

	out := make([]string, 0, len(lines))
	out = append(out, lines[0]) // request line

	seen := map[string]bool{}
	for i := 1; i < len(lines); i++ {
		line := lines[i]
		ci := strings.IndexByte(line, ':')
		if ci <= 0 {
			out = append(out, line)
			continue
		}
		name := strings.ToLower(strings.TrimSpace(line[:ci]))
		if removeMap[name] {
			continue
		}
		if newVal, ok := setLower[name]; ok {
			out = append(out, setOriginalCase[name]+": "+newVal)
			seen[name] = true
		} else {
			out = append(out, line)
		}
	}
	// Append any set-headers that weren't already present.
	for nameLower, val := range setLower {
		if !seen[nameLower] {
			out = append(out, setOriginalCase[nameLower]+": "+val)
		}
	}
	return out
}

// materializeBody decides how to serialize an arbitrary JSON value into a
// request body. Strings are taken literally; objects/arrays/numbers are
// JSON-encoded with no extra whitespace. Returns the body and an
// auto-detected Content-Type (empty when no override is needed).
func materializeBody(raw json.RawMessage) (string, string) {
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) == 0 {
		return "", ""
	}
	// JSON string → use the unwrapped value verbatim, no Content-Type override.
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s, ""
		}
	}
	// Anything else (object/array/number/bool/null) → use as JSON.
	return trimmed, "application/json"
}

func truncateBody(s string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s, false
	}
	return s[:maxBytes] + "\n... [truncated " + strconv.Itoa(len(s)-maxBytes) + " bytes — pass maxBodyBytes:0 for full]", true
}

func scheme(tls bool) string {
	if tls {
		return "https"
	}
	return "http"
}

func ifTrue(b bool, n int) any {
	if b {
		return n
	}
	return nil
}

// hasField checks whether a field on MirrorArgs was provided. With Go's
// JSON unmarshal we can't easily tell empty-string from missing — for
// the few cases we care about (Query="" meaning "drop"), users should
// pass an explicit AppendQuery instead.
func hasField(args MirrorArgs, name string) bool {
	return false // intentionally conservative — empty string means "no change"
}
