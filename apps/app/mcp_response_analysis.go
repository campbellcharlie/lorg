package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/campbellcharlie/lorg/internal/utils"
	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// Input schemas (struct-based, type-safe)
// ---------------------------------------------------------------------------

type ExtractRegexArgs struct {
	Content    string `json:"content" jsonschema:"required" jsonschema_description:"Content to search in"`
	Pattern    string `json:"pattern" jsonschema:"required" jsonschema_description:"Regex pattern (with optional capture groups)"`
	MaxMatches int    `json:"maxMatches" jsonschema:"required" jsonschema_description:"Max matches to return"`
	Group      int    `json:"group,omitempty" jsonschema_description:"Capture group to extract (0=full match, default)"`
}

type ExtractJsonPathArgs struct {
	Content string `json:"content" jsonschema:"required" jsonschema_description:"JSON string to extract from"`
	Path    string `json:"path" jsonschema:"required" jsonschema_description:"Dot-notation path (e.g. data.users.0.name or data.users.*.email)"`
}

type ExtractBetweenArgs struct {
	Content        string `json:"content" jsonschema:"required" jsonschema_description:"Content to search in"`
	StartDelimiter string `json:"startDelimiter" jsonschema:"required" jsonschema_description:"Start delimiter"`
	EndDelimiter   string `json:"endDelimiter" jsonschema:"required" jsonschema_description:"End delimiter"`
	MaxMatches     int    `json:"maxMatches" jsonschema:"required" jsonschema_description:"Max extractions"`
}

type CompareResponsesArgs struct {
	Response1     string   `json:"response1" jsonschema:"required" jsonschema_description:"First HTTP response (raw)"`
	Response2     string   `json:"response2" jsonschema:"required" jsonschema_description:"Second HTTP response (raw)"`
	IgnoreHeaders []string `json:"ignoreHeaders,omitempty" jsonschema_description:"Header names to ignore in comparison (e.g. Date, Set-Cookie)"`
}

type AnalyzeResponseArgs struct {
	Response string `json:"response" jsonschema:"required" jsonschema_description:"Raw HTTP response to analyze"`
}

type AnalyzeResponseVariationsArgs struct {
	Responses []string `json:"responses" jsonschema:"required" jsonschema_description:"Multiple raw HTTP responses to compare for variations"`
}

type AnalyzeResponseKeywordsArgs struct {
	Responses []string `json:"responses" jsonschema:"required" jsonschema_description:"Raw HTTP responses to check"`
	Keywords  []string `json:"keywords" jsonschema:"required" jsonschema_description:"Keywords to search for in response bodies"`
}

// ---------------------------------------------------------------------------
// Helper: parse raw HTTP response into components
// ---------------------------------------------------------------------------

func parseHTTPResponse(raw string) (statusLine string, statusCode int, headers map[string]string, body string) {
	// Split on double newline (handle both \r\n\r\n and \n\n)
	parts := strings.SplitN(raw, "\r\n\r\n", 2)
	if len(parts) < 2 {
		parts = strings.SplitN(raw, "\n\n", 2)
	}

	headerSection := ""
	if len(parts) >= 1 {
		headerSection = parts[0]
	}
	if len(parts) >= 2 {
		body = parts[1]
	}

	lines := strings.Split(headerSection, "\n")
	headers = make(map[string]string)

	if len(lines) > 0 {
		statusLine = strings.TrimSpace(lines[0])
		// Parse status code from "HTTP/1.1 200 OK"
		fields := strings.Fields(statusLine)
		if len(fields) >= 2 {
			fmt.Sscanf(fields[1], "%d", &statusCode)
		}
	}

	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if idx := strings.Index(line, ":"); idx > 0 {
			name := strings.TrimSpace(line[:idx])
			value := strings.TrimSpace(line[idx+1:])
			headers[name] = value
		}
	}

	return
}

// ---------------------------------------------------------------------------
// Helper: walk a JSON value by dot-notation path
// ---------------------------------------------------------------------------

func walkJSONPath(data any, segments []string) []any {
	if len(segments) == 0 {
		return []any{data}
	}

	seg := segments[0]
	rest := segments[1:]

	switch v := data.(type) {
	case map[string]any:
		child, ok := v[seg]
		if !ok {
			return nil
		}
		return walkJSONPath(child, rest)

	case []any:
		if seg == "*" {
			var results []any
			for _, elem := range v {
				results = append(results, walkJSONPath(elem, rest)...)
			}
			return results
		}
		idx, err := strconv.Atoi(seg)
		if err != nil || idx < 0 || idx >= len(v) {
			return nil
		}
		return walkJSONPath(v[idx], rest)

	default:
		return nil
	}
}

// ---------------------------------------------------------------------------
// Tool handlers
// ---------------------------------------------------------------------------

func (backend *Backend) extractRegexHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ExtractRegexArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	re, err := regexp.Compile(args.Pattern)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid regex pattern: %v", err)), nil
	}

	allMatches := re.FindAllStringSubmatch(args.Content, args.MaxMatches)

	group := args.Group
	matches := make([]string, 0, len(allMatches))
	for _, m := range allMatches {
		if group < 0 || group >= len(m) {
			continue
		}
		matches = append(matches, m[group])
	}

	return mcpJSONResult(map[string]any{
		"matches": matches,
		"count":   len(matches),
	})
}

func (backend *Backend) extractJsonPathHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ExtractJsonPathArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	var data any
	if err := json.Unmarshal([]byte(args.Content), &data); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid JSON: %v", err)), nil
	}

	segments := strings.Split(args.Path, ".")
	values := walkJSONPath(data, segments)

	return mcpJSONResult(map[string]any{
		"values": values,
		"count":  len(values),
	})
}

func (backend *Backend) extractBetweenHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ExtractBetweenArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	matches := make([]string, 0)
	remaining := args.Content

	for len(matches) < args.MaxMatches {
		startIdx := strings.Index(remaining, args.StartDelimiter)
		if startIdx == -1 {
			break
		}
		afterStart := startIdx + len(args.StartDelimiter)
		endIdx := strings.Index(remaining[afterStart:], args.EndDelimiter)
		if endIdx == -1 {
			break
		}
		matches = append(matches, remaining[afterStart:afterStart+endIdx])
		remaining = remaining[afterStart+endIdx+len(args.EndDelimiter):]
	}

	return mcpJSONResult(map[string]any{
		"matches": matches,
		"count":   len(matches),
	})
}

func (backend *Backend) compareResponsesHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args CompareResponsesArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	status1, code1, headers1, body1 := parseHTTPResponse(args.Response1)
	status2, code2, headers2, body2 := parseHTTPResponse(args.Response2)

	statusMatch := code1 == code2

	// Build set of headers to ignore (case-insensitive)
	ignoreSet := make(map[string]bool, len(args.IgnoreHeaders))
	for _, h := range args.IgnoreHeaders {
		ignoreSet[strings.ToLower(h)] = true
	}

	// Compare headers
	type headerDiff struct {
		Name   string `json:"name"`
		Value1 string `json:"value1"`
		Value2 string `json:"value2"`
	}
	var headerDiffs []headerDiff

	allHeaders := make(map[string]bool)
	for name := range headers1 {
		allHeaders[name] = true
	}
	for name := range headers2 {
		allHeaders[name] = true
	}

	for name := range allHeaders {
		if ignoreSet[strings.ToLower(name)] {
			continue
		}
		v1 := headers1[name]
		v2 := headers2[name]
		if v1 != v2 {
			headerDiffs = append(headerDiffs, headerDiff{
				Name:   name,
				Value1: v1,
				Value2: v2,
			})
		}
	}

	// Body length diff
	bodyLengthDiff := len(body1) - len(body2)

	// Simple line-level diff of body (cap at 50 diff lines)
	type bodyDiffEntry struct {
		Type string `json:"type"`
		Line string `json:"line"`
	}

	lines1 := strings.Split(body1, "\n")
	lines2 := strings.Split(body2, "\n")

	lineSet1 := make(map[string]int, len(lines1))
	for _, l := range lines1 {
		lineSet1[l]++
	}
	lineSet2 := make(map[string]int, len(lines2))
	for _, l := range lines2 {
		lineSet2[l]++
	}

	var bodyDiffs []bodyDiffEntry
	maxDiffs := 50

	// Lines in response1 but not in response2
	for _, l := range lines1 {
		if len(bodyDiffs) >= maxDiffs {
			break
		}
		if lineSet2[l] > 0 {
			lineSet2[l]--
		} else {
			bodyDiffs = append(bodyDiffs, bodyDiffEntry{Type: "removed", Line: l})
		}
	}

	// Reset lineSet1 for the second pass
	lineSet1Reset := make(map[string]int, len(lines1))
	for _, l := range lines1 {
		lineSet1Reset[l]++
	}

	// Lines in response2 but not in response1
	for _, l := range lines2 {
		if len(bodyDiffs) >= maxDiffs {
			break
		}
		if lineSet1Reset[l] > 0 {
			lineSet1Reset[l]--
		} else {
			bodyDiffs = append(bodyDiffs, bodyDiffEntry{Type: "added", Line: l})
		}
	}

	identical := statusMatch && len(headerDiffs) == 0 && len(bodyDiffs) == 0

	return mcpJSONResult(map[string]any{
		"statusMatch":    statusMatch,
		"status1":        status1,
		"status2":        status2,
		"headerDiffs":    headerDiffs,
		"bodyLengthDiff": bodyLengthDiff,
		"bodyDiffs":      bodyDiffs,
		"identical":      identical,
	})
}

func (backend *Backend) analyzeResponseHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args AnalyzeResponseArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	_, statusCode, headers, _ := parseHTTPResponse(args.Response)

	// Security headers check
	securityHeaders := []string{
		"X-Frame-Options",
		"Content-Security-Policy",
		"Strict-Transport-Security",
		"X-Content-Type-Options",
		"X-XSS-Protection",
		"Referrer-Policy",
		"Permissions-Policy",
		"Access-Control-Allow-Origin",
	}

	present := make([]map[string]string, 0)
	missing := make([]string, 0)

	for _, sh := range securityHeaders {
		found := false
		for name, value := range headers {
			if strings.EqualFold(name, sh) {
				present = append(present, map[string]string{"name": name, "value": value})
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, sh)
		}
	}

	// Parse Set-Cookie headers.
	// Note: parseHTTPResponse collapses duplicate header names, so we
	// re-scan the raw header section to capture all Set-Cookie lines.
	type cookieInfo struct {
		Name     string `json:"name"`
		HttpOnly bool   `json:"httpOnly"`
		Secure   bool   `json:"secure"`
		SameSite string `json:"sameSite"`
	}

	var cookies []cookieInfo

	rawParts := strings.SplitN(args.Response, "\r\n\r\n", 2)
	if len(rawParts) < 2 {
		rawParts = strings.SplitN(args.Response, "\n\n", 2)
	}
	if len(rawParts) >= 1 {
		headerLines := strings.Split(rawParts[0], "\n")
		for _, line := range headerLines {
			line = strings.TrimSpace(line)
			if idx := strings.Index(line, ":"); idx > 0 {
				name := strings.TrimSpace(line[:idx])
				value := strings.TrimSpace(line[idx+1:])
				if strings.EqualFold(name, "Set-Cookie") {
					ci := cookieInfo{}
					cookieParts := strings.Split(value, ";")
					if len(cookieParts) > 0 {
						nameVal := strings.SplitN(strings.TrimSpace(cookieParts[0]), "=", 2)
						if len(nameVal) >= 1 {
							ci.Name = nameVal[0]
						}
					}
					lowerValue := strings.ToLower(value)
					ci.HttpOnly = strings.Contains(lowerValue, "httponly")
					ci.Secure = strings.Contains(lowerValue, "secure")
					if ssIdx := strings.Index(lowerValue, "samesite="); ssIdx >= 0 {
						rest := lowerValue[ssIdx+len("samesite="):]
						if semi := strings.Index(rest, ";"); semi >= 0 {
							ci.SameSite = strings.TrimSpace(rest[:semi])
						} else {
							ci.SameSite = strings.TrimSpace(rest)
						}
					}
					cookies = append(cookies, ci)
				}
			}
		}
	}

	// Technology hints
	techHeaders := []string{"Server", "X-Powered-By", "X-AspNet-Version", "X-Generator"}
	techHints := make(map[string]string)
	for _, th := range techHeaders {
		for name, value := range headers {
			if strings.EqualFold(name, th) {
				techHints[th] = value
				break
			}
		}
	}

	// Content-Type and Content-Length
	contentType := ""
	contentLength := 0
	for name, value := range headers {
		if strings.EqualFold(name, "Content-Type") {
			contentType = value
		}
		if strings.EqualFold(name, "Content-Length") {
			fmt.Sscanf(value, "%d", &contentLength)
		}
	}

	return mcpJSONResult(map[string]any{
		"statusCode":    statusCode,
		"contentType":   contentType,
		"contentLength": contentLength,
		"securityHeaders": map[string]any{
			"present": present,
			"missing": missing,
		},
		"cookies":   cookies,
		"techHints": techHints,
	})
}

func (backend *Backend) analyzeResponseVariationsHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args AnalyzeResponseVariationsArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	if len(args.Responses) == 0 {
		return mcp.NewToolResultError("at least one response is required"), nil
	}

	type parsed struct {
		statusCode    int
		contentLength string
		headers       map[string]string
		bodyHash      string
	}

	responses := make([]parsed, 0, len(args.Responses))
	allHeaderNames := make(map[string]bool)

	for _, raw := range args.Responses {
		_, code, hdrs, body := parseHTTPResponse(raw)
		h := sha256.Sum256([]byte(body))
		cl := ""
		for name, value := range hdrs {
			if strings.EqualFold(name, "Content-Length") {
				cl = value
			}
			allHeaderNames[name] = true
		}
		responses = append(responses, parsed{
			statusCode:    code,
			contentLength: cl,
			headers:       hdrs,
			bodyHash:      fmt.Sprintf("%x", h),
		})
	}

	type attrResult struct {
		Attr   string   `json:"attr"`
		Value  string   `json:"value,omitempty"`
		Values []string `json:"values,omitempty"`
	}

	var invariant []attrResult
	var varying []attrResult

	// Check status code
	statusSame := true
	statusValues := make(map[string]bool)
	for _, r := range responses {
		s := strconv.Itoa(r.statusCode)
		statusValues[s] = true
		if r.statusCode != responses[0].statusCode {
			statusSame = false
		}
	}
	if statusSame {
		invariant = append(invariant, attrResult{Attr: "statusCode", Value: strconv.Itoa(responses[0].statusCode)})
	} else {
		vals := make([]string, 0, len(statusValues))
		for v := range statusValues {
			vals = append(vals, v)
		}
		varying = append(varying, attrResult{Attr: "statusCode", Values: vals})
	}

	// Check content-length
	clSame := true
	clValues := make(map[string]bool)
	for _, r := range responses {
		clValues[r.contentLength] = true
		if r.contentLength != responses[0].contentLength {
			clSame = false
		}
	}
	if clSame {
		invariant = append(invariant, attrResult{Attr: "content-length", Value: responses[0].contentLength})
	} else {
		vals := make([]string, 0, len(clValues))
		for v := range clValues {
			vals = append(vals, v)
		}
		varying = append(varying, attrResult{Attr: "content-length", Values: vals})
	}

	// Check body hash
	bhSame := true
	bhValues := make(map[string]bool)
	for _, r := range responses {
		bhValues[r.bodyHash] = true
		if r.bodyHash != responses[0].bodyHash {
			bhSame = false
		}
	}
	if bhSame {
		invariant = append(invariant, attrResult{Attr: "bodyHash", Value: responses[0].bodyHash})
	} else {
		vals := make([]string, 0, len(bhValues))
		for v := range bhValues {
			vals = append(vals, v)
		}
		varying = append(varying, attrResult{Attr: "bodyHash", Values: vals})
	}

	// Check each header
	for headerName := range allHeaderNames {
		same := true
		headerValues := make(map[string]bool)
		firstVal := responses[0].headers[headerName]
		for _, r := range responses {
			v := r.headers[headerName]
			headerValues[v] = true
			if v != firstVal {
				same = false
			}
		}
		if same {
			invariant = append(invariant, attrResult{Attr: headerName, Value: firstVal})
		} else {
			vals := make([]string, 0, len(headerValues))
			for v := range headerValues {
				vals = append(vals, v)
			}
			varying = append(varying, attrResult{Attr: headerName, Values: vals})
		}
	}

	return mcpJSONResult(map[string]any{
		"totalResponses": len(args.Responses),
		"invariant":      invariant,
		"varying":        varying,
	})
}

func (backend *Backend) analyzeResponseKeywordsHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args AnalyzeResponseKeywordsArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	if len(args.Responses) == 0 {
		return mcp.NewToolResultError("at least one response is required"), nil
	}

	// Extract bodies from all responses
	bodies := make([]string, 0, len(args.Responses))
	for _, raw := range args.Responses {
		_, _, _, body := parseHTTPResponse(raw)
		bodies = append(bodies, strings.ToLower(body))
	}

	type keywordResult struct {
		Keyword    string `json:"keyword"`
		PresentIn  []int  `json:"presentIn"`
		AbsentIn   []int  `json:"absentIn"`
		Consistent bool   `json:"consistent"`
	}

	keywords := make([]keywordResult, 0, len(args.Keywords))
	for _, kw := range args.Keywords {
		lowerKW := strings.ToLower(kw)
		kr := keywordResult{
			Keyword:   kw,
			PresentIn: make([]int, 0),
			AbsentIn:  make([]int, 0),
		}
		for i, body := range bodies {
			if strings.Contains(body, lowerKW) {
				kr.PresentIn = append(kr.PresentIn, i)
			} else {
				kr.AbsentIn = append(kr.AbsentIn, i)
			}
		}
		// Consistent means the keyword is either present in all or absent from all
		kr.Consistent = len(kr.PresentIn) == 0 || len(kr.AbsentIn) == 0
		keywords = append(keywords, kr)
	}

	return mcpJSONResult(map[string]any{
		"keywords":       keywords,
		"totalResponses": len(args.Responses),
	})
}

// ---------------------------------------------------------------------------
// compareTrafficById: compare two traffic entries by PocketBase ID
// ---------------------------------------------------------------------------

type CompareTrafficByIdArgs struct {
	ID1           string   `json:"id1" jsonschema:"required" jsonschema_description:"First request activeID (from PocketBase)"`
	ID2           string   `json:"id2" jsonschema:"required" jsonschema_description:"Second request activeID (from PocketBase)"`
	IgnoreHeaders []string `json:"ignoreHeaders,omitempty" jsonschema_description:"Headers to ignore in comparison"`
}

func (backend *Backend) compareTrafficByIdHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args CompareTrafficByIdArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// Pad IDs to 15 chars with leading underscores (same as getRequestResponseFromIDHandler)
	id1 := utils.FormatStringID(args.ID1, 15)
	id2 := utils.FormatStringID(args.ID2, 15)

	// Fetch raw responses
	respRec1, _ := backend.DB.FindRecordById("_resp", id1)
	respRec2, _ := backend.DB.FindRecordById("_resp", id2)

	if respRec1 == nil {
		return mcp.NewToolResultError(fmt.Sprintf("no response found for ID1: %s", id1)), nil
	}
	if respRec2 == nil {
		return mcp.NewToolResultError(fmt.Sprintf("no response found for ID2: %s", id2)), nil
	}

	rawResp1 := respRec1.GetString("raw")
	rawResp2 := respRec2.GetString("raw")

	// Fetch raw requests for context
	reqRec1, _ := backend.DB.FindRecordById("_req", id1)
	reqRec2, _ := backend.DB.FindRecordById("_req", id2)

	// Extract method and path from request lines for context
	req1Method, req1Path := "", ""
	req2Method, req2Path := "", ""
	if reqRec1 != nil {
		raw := reqRec1.GetString("raw")
		if firstLine := strings.SplitN(raw, "\r\n", 2); len(firstLine) > 0 {
			fields := strings.Fields(firstLine[0])
			if len(fields) >= 2 {
				req1Method = fields[0]
				req1Path = fields[1]
			}
		}
	}
	if reqRec2 != nil {
		raw := reqRec2.GetString("raw")
		if firstLine := strings.SplitN(raw, "\r\n", 2); len(firstLine) > 0 {
			fields := strings.Fields(firstLine[0])
			if len(fields) >= 2 {
				req2Method = fields[0]
				req2Path = fields[1]
			}
		}
	}

	// Parse both responses
	status1, code1, headers1, body1 := parseHTTPResponse(rawResp1)
	status2, code2, headers2, body2 := parseHTTPResponse(rawResp2)

	statusMatch := code1 == code2

	// Build set of headers to ignore (case-insensitive)
	ignoreSet := make(map[string]bool, len(args.IgnoreHeaders))
	for _, h := range args.IgnoreHeaders {
		ignoreSet[strings.ToLower(h)] = true
	}

	// Compare headers
	type headerDiff struct {
		Name   string `json:"name"`
		Value1 string `json:"value1"`
		Value2 string `json:"value2"`
	}
	var headerDiffs []headerDiff

	allHeaders := make(map[string]bool)
	for name := range headers1 {
		allHeaders[name] = true
	}
	for name := range headers2 {
		allHeaders[name] = true
	}

	for name := range allHeaders {
		if ignoreSet[strings.ToLower(name)] {
			continue
		}
		v1 := headers1[name]
		v2 := headers2[name]
		if v1 != v2 {
			headerDiffs = append(headerDiffs, headerDiff{
				Name:   name,
				Value1: v1,
				Value2: v2,
			})
		}
	}

	// Body length diff
	bodyLengthDiff := len(body1) - len(body2)

	// Simple line-level diff of body (cap at 50 diff lines)
	type bodyDiffEntry struct {
		Type string `json:"type"`
		Line string `json:"line"`
	}

	lines1 := strings.Split(body1, "\n")
	lines2 := strings.Split(body2, "\n")

	lineSet1 := make(map[string]int, len(lines1))
	for _, l := range lines1 {
		lineSet1[l]++
	}
	lineSet2 := make(map[string]int, len(lines2))
	for _, l := range lines2 {
		lineSet2[l]++
	}

	var bodyDiffs []bodyDiffEntry
	maxDiffs := 50

	// Lines in response1 but not in response2
	for _, l := range lines1 {
		if len(bodyDiffs) >= maxDiffs {
			break
		}
		if lineSet2[l] > 0 {
			lineSet2[l]--
		} else {
			bodyDiffs = append(bodyDiffs, bodyDiffEntry{Type: "removed", Line: l})
		}
	}

	// Reset lineSet1 for the second pass
	lineSet1Reset := make(map[string]int, len(lines1))
	for _, l := range lines1 {
		lineSet1Reset[l]++
	}

	// Lines in response2 but not in response1
	for _, l := range lines2 {
		if len(bodyDiffs) >= maxDiffs {
			break
		}
		if lineSet1Reset[l] > 0 {
			lineSet1Reset[l]--
		} else {
			bodyDiffs = append(bodyDiffs, bodyDiffEntry{Type: "added", Line: l})
		}
	}

	identical := statusMatch && len(headerDiffs) == 0 && len(bodyDiffs) == 0

	return mcpJSONResult(map[string]any{
		"statusMatch":    statusMatch,
		"status1":        status1,
		"status2":        status2,
		"headerDiffs":    headerDiffs,
		"bodyLengthDiff": bodyLengthDiff,
		"bodyDiffs":      bodyDiffs,
		"identical":      identical,
		"request1Method": req1Method,
		"request1Path":   req1Path,
		"request2Method": req2Method,
		"request2Path":   req2Path,
	})
}
