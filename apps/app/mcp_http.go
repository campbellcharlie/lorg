package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// Input schemas
// ---------------------------------------------------------------------------

// SendHttpRequestArgs defines the parameters for the structured HTTP request tool.
// This is the primary tool for sending HTTP requests via lorg -- it accepts
// structured parameters rather than a raw HTTP string.
type SendHttpRequestArgs struct {
	Method          string            `json:"method" jsonschema:"required" jsonschema_description:"HTTP method (GET, POST, PUT, DELETE, PATCH, etc.)"`
	URL             string            `json:"url" jsonschema:"required" jsonschema_description:"Full URL (e.g. http://example.com:8000/path?q=1)"`
	Headers         map[string]string `json:"headers,omitempty" jsonschema_description:"Additional request headers"`
	Body            string            `json:"body,omitempty" jsonschema_description:"Request body"`
	InjectSession   bool              `json:"injectSession,omitempty" jsonschema_description:"Auto-inject active session cookies, CSRF token, and custom headers"`
	CaptureSession  bool              `json:"captureSession,omitempty" jsonschema_description:"Auto-extract Set-Cookie and CSRF tokens from response into active session"`
	FollowRedirects bool              `json:"followRedirects,omitempty" jsonschema_description:"Follow 3xx redirects (up to 10 hops)"`
	ExtractRegex    string            `json:"extractRegex,omitempty" jsonschema_description:"Regex to extract from response body"`
	ExtractGroup    int               `json:"extractGroup,omitempty" jsonschema_description:"Capture group for extraction (default: 1)"`
	BodyOnly        bool              `json:"bodyOnly,omitempty" jsonschema_description:"Return only response body, not headers"`
	MaxBodyLength   int               `json:"maxBodyLength,omitempty" jsonschema_description:"Truncate response body to this many characters (0 = no limit). Useful for large HTML responses that would overwhelm agent context."`
	HeadersOnly     bool              `json:"headersOnly,omitempty" jsonschema_description:"Return only response headers, no body. Overrides bodyOnly."`
	Note            string            `json:"note,omitempty" jsonschema_description:"Note to attach to request"`
}

// ReplayFromDbArgs defines the parameters for replaying a request stored in
// the project SQLite database.
type ReplayFromDbArgs struct {
	RequestID     int               `json:"requestId" jsonschema:"required" jsonschema_description:"request_id from project SQLite DB"`
	ModifyHeaders map[string]string `json:"modifyHeaders,omitempty" jsonschema_description:"Headers to add or replace"`
	ModifyBody    string            `json:"modifyBody,omitempty" jsonschema_description:"Replacement request body"`
	InjectSession bool              `json:"injectSession,omitempty" jsonschema_description:"Inject active session cookies/headers"`
	Note          string            `json:"note,omitempty" jsonschema_description:"Note for replayed request"`
}

// ---------------------------------------------------------------------------
// sendHttpRequest handler
// ---------------------------------------------------------------------------

func (backend *Backend) sendHttpRequestHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args SendHttpRequestArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	if args.Method == "" {
		return mcp.NewToolResultError("method is required"), nil
	}
	if args.URL == "" {
		return mcp.NewToolResultError("url is required"), nil
	}

	// 1. Parse URL
	parsedURL, err := url.Parse(args.URL)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid URL: %v", err)), nil
	}

	scheme := strings.ToLower(parsedURL.Scheme)
	if scheme == "" {
		scheme = "http"
	}
	tls := scheme == "https"

	host := parsedURL.Hostname()
	if host == "" {
		return mcp.NewToolResultError("URL must contain a hostname"), nil
	}

	portStr := parsedURL.Port()
	if portStr == "" {
		if tls {
			portStr = "443"
		} else {
			portStr = "80"
		}
	}
	port, _ := strconv.Atoi(portStr)

	pathAndQuery := parsedURL.RequestURI()
	if pathAndQuery == "" {
		pathAndQuery = "/"
	}

	// 2. Build raw HTTP request
	var rawBuilder strings.Builder
	rawBuilder.WriteString(fmt.Sprintf("%s %s HTTP/1.1\r\n", strings.ToUpper(args.Method), pathAndQuery))

	// Host header: include port only for non-default ports
	hostHeader := host
	if (tls && port != 443) || (!tls && port != 80) {
		hostHeader = fmt.Sprintf("%s:%d", host, port)
	}
	rawBuilder.WriteString(fmt.Sprintf("Host: %s\r\n", hostHeader))

	// Add user-supplied headers
	for k, v := range args.Headers {
		rawBuilder.WriteString(fmt.Sprintf("%s: %s\r\n", k, v))
	}

	// Normalize body line endings before calculating Content-Length.
	// normalizeCRLF is applied to the full request later; pre-normalizing the
	// body here ensures Content-Length matches the actual bytes transmitted when
	// the body contains bare \n characters.
	body := normalizeCRLF(args.Body)

	// Add Content-Length if there is a body
	if body != "" {
		if !hasHeader(rawBuilder.String(), "Content-Length") {
			rawBuilder.WriteString(fmt.Sprintf("Content-Length: %d\r\n", len(body)))
		}
	}

	// Terminate headers
	rawBuilder.WriteString("\r\n")

	// Append body (already CRLF-normalized)
	if body != "" {
		rawBuilder.WriteString(body)
	}

	rawReq := rawBuilder.String()

	// 3. Session injection (if injectSession)
	if args.InjectSession {
		rawReq = backend.injectSessionAndCSRF(rawReq, args.Method, body)
	}

	// 4. CRLF normalize + default headers (UA, Accept, Connection: close)
	rawReq = normalizeCRLF(rawReq)
	rawReq = injectDefaultHeaders(rawReq)

	// 5. Send via sendRepeaterLogic
	start := time.Now()
	resp, err := backend.sendRepeaterLogic(&RepeaterSendRequest{
		Host:        host,
		Port:        portStr,
		TLS:         tls,
		Request:     rawReq,
		Timeout:     30,
		HTTP2:       false,
		Index:       0,
		Url:         fmt.Sprintf("%s://%s:%d", scheme, host, port),
		Note:        args.Note,
		GeneratedBy: "ai/mcp/http",
	})
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	elapsed := time.Since(start)

	rawResponse := resp.Response

	// 6. Session capture (if captureSession)
	if args.CaptureSession {
		backend.captureSessionFromResponse(rawResponse)
	}

	// 7. Follow redirects (if followRedirects)
	type redirectHop struct {
		URL    string `json:"url"`
		Status int    `json:"status"`
	}
	var redirectChain []redirectHop

	if args.FollowRedirects {
		currentResponse := rawResponse
		currentURL := parsedURL
		currentMethod := strings.ToUpper(args.Method)
		currentBody := body
		currentHost := host
		currentPort := port
		currentPortStr := portStr
		currentTLS := tls
		currentScheme := scheme

		for i := 0; i < 10; i++ {
			statusCode := parseStatusCode(currentResponse)
			if statusCode < 300 || statusCode > 399 {
				break
			}

			location := extractHeaderValue(currentResponse, "Location")
			if location == "" {
				break
			}

			// Resolve relative URL
			locURL, locErr := url.Parse(location)
			if locErr != nil {
				break
			}
			resolvedURL := currentURL.ResolveReference(locURL)

			redirectChain = append(redirectChain, redirectHop{
				URL:    resolvedURL.String(),
				Status: statusCode,
			})

			// For 301/302/303: switch to GET, drop body
			// For 307/308: preserve method and body
			if statusCode == 301 || statusCode == 302 || statusCode == 303 {
				currentMethod = "GET"
				currentBody = ""
			}

			// Update connection params from resolved URL
			if s := strings.ToLower(resolvedURL.Scheme); s != "" {
				currentScheme = s
			}
			currentTLS = currentScheme == "https"
			currentHost = resolvedURL.Hostname()
			if currentHost == "" {
				currentHost = host // keep original
			}
			currentPortStr = resolvedURL.Port()
			if currentPortStr == "" {
				if currentTLS {
					currentPortStr = "443"
				} else {
					currentPortStr = "80"
				}
			}
			currentPort, _ = strconv.Atoi(currentPortStr)

			redirectPath := resolvedURL.RequestURI()
			if redirectPath == "" {
				redirectPath = "/"
			}

			// Build redirect request
			var redirBuilder strings.Builder
			redirBuilder.WriteString(fmt.Sprintf("%s %s HTTP/1.1\r\n", currentMethod, redirectPath))

			redirHostHeader := currentHost
			if (currentTLS && currentPort != 443) || (!currentTLS && currentPort != 80) {
				redirHostHeader = fmt.Sprintf("%s:%d", currentHost, currentPort)
			}
			redirBuilder.WriteString(fmt.Sprintf("Host: %s\r\n", redirHostHeader))

			// Carry over original headers except Host and Content-Length
			for k, v := range args.Headers {
				kLower := strings.ToLower(k)
				if kLower == "host" || kLower == "content-length" {
					continue
				}
				redirBuilder.WriteString(fmt.Sprintf("%s: %s\r\n", k, v))
			}

			if currentBody != "" {
				redirBuilder.WriteString(fmt.Sprintf("Content-Length: %d\r\n", len(currentBody)))
			}
			redirBuilder.WriteString("\r\n")
			if currentBody != "" {
				redirBuilder.WriteString(currentBody)
			}

			redirRaw := normalizeCRLF(redirBuilder.String())
			redirRaw = injectDefaultHeaders(redirRaw)

			redirResp, redirErr := backend.sendRepeaterLogic(&RepeaterSendRequest{
				Host:        currentHost,
				Port:        currentPortStr,
				TLS:         currentTLS,
				Request:     redirRaw,
				Timeout:     30,
				HTTP2:       false,
				Index:       0,
				Url:         fmt.Sprintf("%s://%s:%d", currentScheme, currentHost, currentPort),
				Note:        args.Note,
				GeneratedBy: "ai/mcp/http/redirect",
			})
			if redirErr != nil {
				break
			}

			currentResponse = redirResp.Response
			currentURL = resolvedURL

			// Capture session from each hop if requested
			if args.CaptureSession {
				backend.captureSessionFromResponse(currentResponse)
			}
		}

		// Use the final response
		rawResponse = currentResponse
	}

	// 8. Regex extraction (if extractRegex)
	var extractedValue string
	if args.ExtractRegex != "" {
		_, body := splitResponseHeaders(rawResponse)
		re, reErr := regexp.Compile(args.ExtractRegex)
		if reErr == nil {
			matches := re.FindStringSubmatch(body)
			group := args.ExtractGroup
			if group <= 0 {
				group = 1
			}
			if len(matches) > group {
				extractedValue = matches[group]
			}
		}
	}

	// 9. Parse response headers into a map for structured output
	parsedHeaders := parseResponseHeaders(rawResponse)
	statusCode := parseStatusCode(rawResponse)

	// Build output response text
	outputResponse := rawResponse
	if args.HeadersOnly {
		outputResponse, _ = splitResponseHeaders(rawResponse)
	} else if args.BodyOnly {
		_, outputResponse = splitResponseHeaders(rawResponse)
	} else if args.MaxBodyLength > 0 {
		headerPart, bodyPart := splitResponseHeaders(rawResponse)
		if len(bodyPart) > args.MaxBodyLength {
			bodyPart = bodyPart[:args.MaxBodyLength] + fmt.Sprintf("\n...[truncated, showing %d of %d bytes]", args.MaxBodyLength, len(bodyPart))
		}
		outputResponse = headerPart + "\r\n\r\n" + bodyPart
	}

	result := map[string]any{
		"statusCode": statusCode,
		"response":   outputResponse,
		"headers":    parsedHeaders,
		"requestId":  resp.UserData.ID,
		"time":       elapsed.String(),
	}
	if extractedValue != "" {
		result["extractedValue"] = extractedValue
	}
	if len(redirectChain) > 0 {
		result["redirectChain"] = redirectChain
	}

	return mcpJSONResult(result)
}

// ---------------------------------------------------------------------------
// replayFromDb handler
// ---------------------------------------------------------------------------

func (backend *Backend) replayFromDbHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ReplayFromDbArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// 1. Fetch request from project SQLite DB
	dbReq, err := getRequestFromProjectDB(args.RequestID)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// 2. Reconstruct raw HTTP request from stored headers + body
	var rawBuilder strings.Builder
	rawBuilder.WriteString(dbReq.RequestHeaders)

	// 3. Apply header modifications
	rawStr := rawBuilder.String()
	for k, v := range args.ModifyHeaders {
		rawStr = setHeaderValue(rawStr, k, v)
	}

	// 4. Apply body modification + update Content-Length
	body := string(dbReq.RequestBody)
	if args.ModifyBody != "" {
		body = args.ModifyBody
	}

	// Ensure header section ends with \r\n\r\n, then append body
	rawStr = strings.TrimRight(rawStr, "\r\n")
	rawStr += "\r\n"

	// Update Content-Length if body is present
	if body != "" {
		rawStr = setHeaderValue(rawStr, "Content-Length", strconv.Itoa(len(body)))
	}
	rawStr += "\r\n" + body

	// 5. Session injection if requested
	if args.InjectSession {
		injected, injErr := backend.injectSessionIntoRequest(rawStr)
		if injErr == nil {
			rawStr = injected
		}
	}

	// 6. CRLF normalize
	rawStr = normalizeCRLF(rawStr)

	// Derive connection params
	tls := strings.ToLower(dbReq.Protocol) == "https"
	portStr := strconv.Itoa(dbReq.Port)
	scheme := dbReq.Protocol
	if scheme == "" {
		scheme = "http"
	}

	// 7. Send via sendRepeaterLogic
	resp, err := backend.sendRepeaterLogic(&RepeaterSendRequest{
		Host:        dbReq.Host,
		Port:        portStr,
		TLS:         tls,
		Request:     rawStr,
		Timeout:     30,
		HTTP2:       false,
		Index:       0,
		Url:         fmt.Sprintf("%s://%s:%d", scheme, dbReq.Host, dbReq.Port),
		Note:        args.Note,
		GeneratedBy: "ai/mcp/replay",
	})
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// 8. Return response
	statusCode := parseStatusCode(resp.Response)
	parsedHeaders := parseResponseHeaders(resp.Response)

	return mcpJSONResult(map[string]any{
		"statusCode":        statusCode,
		"response":          resp.Response,
		"headers":           parsedHeaders,
		"requestId":         resp.UserData.ID,
		"time":              resp.Time,
		"originalRequestId": args.RequestID,
	})
}

// ---------------------------------------------------------------------------
// Project DB request retrieval
// ---------------------------------------------------------------------------

// projectDBRequest holds the fields needed to reconstruct and replay a
// stored HTTP request from the project SQLite database.
type projectDBRequest struct {
	Host            string
	Port            int
	Protocol        string
	Method          string
	Path            string
	RequestHeaders  string
	RequestBody     []byte
	ResponseHeaders string
	ResponseBody    []byte
}

// getRequestFromProjectDB queries the project SQLite DB for a stored request
// by its request_id. The caller receives enough data to reconstruct the raw
// HTTP request for replay.
func getRequestFromProjectDB(requestID int) (*projectDBRequest, error) {
	projectDB.mu.Lock()
	defer projectDB.mu.Unlock()

	if projectDB.db == nil || !projectDB.ready {
		return nil, fmt.Errorf("no active project database")
	}

	var req projectDBRequest
	err := projectDB.db.QueryRow(`
		SELECT t.host, t.port, t.protocol, t.method, t.path,
		       m.request_headers, m.request_body, m.response_headers, m.response_body
		FROM http_traffic t
		JOIN http_messages m ON m.request_id = t.request_id
		WHERE t.request_id = ?`, requestID).Scan(
		&req.Host, &req.Port, &req.Protocol, &req.Method, &req.Path,
		&req.RequestHeaders, &req.RequestBody, &req.ResponseHeaders, &req.ResponseBody,
	)
	if err != nil {
		return nil, fmt.Errorf("request %d not found: %w", requestID, err)
	}
	return &req, nil
}

// ---------------------------------------------------------------------------
// Session capture from response
// ---------------------------------------------------------------------------

// captureSessionFromResponse parses Set-Cookie headers and CSRF tokens from
// a raw HTTP response and stores them in the active session. If no active
// session exists, this is a no-op.
func (backend *Backend) captureSessionFromResponse(rawResponse string) {
	session, err := backend.findActiveSession()
	if err != nil {
		return
	}

	dao := backend.App.Dao()

	// Parse Set-Cookie headers
	headerSection, _ := splitResponseHeaders(rawResponse)
	for _, line := range strings.Split(headerSection, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "set-cookie:") {
			cookieStr := strings.TrimSpace(line[len("set-cookie:"):])
			// Extract name=value (before first ;)
			parts := strings.SplitN(cookieStr, ";", 2)
			kv := strings.SplitN(strings.TrimSpace(parts[0]), "=", 2)
			if len(kv) == 2 {
				cookies, _ := session.Get("cookies").(map[string]any)
				if cookies == nil {
					cookies = make(map[string]any)
				}
				cookies[kv[0]] = kv[1]
				session.Set("cookies", cookies)
			}
		}
	}

	// Extract CSRF tokens from body
	_, body := splitResponseHeaders(rawResponse)
	csrfPatterns := []struct {
		re       *regexp.Regexp
		hasField bool // true if the regex has a field-name capture group before the value
	}{
		{
			re:       regexp.MustCompile(`(?i)<input[^>]*name=["']?(csrf[_\-]?token|_token|csrfmiddlewaretoken|__RequestVerificationToken|_csrf|authenticity_token)["']?[^>]*value=["']?([^"'\s>]+)`),
			hasField: true,
		},
		{
			re:       regexp.MustCompile(`(?i)<meta[^>]*name=["']?csrf-token["']?[^>]*content=["']?([^"']+)`),
			hasField: false,
		},
	}

	for _, pat := range csrfPatterns {
		matches := pat.re.FindStringSubmatch(body)
		if matches == nil {
			continue
		}
		if pat.hasField && len(matches) >= 3 {
			session.Set("csrf_field", matches[1])
			session.Set("csrf_token", matches[2])
		} else if !pat.hasField && len(matches) >= 2 {
			session.Set("csrf_field", "csrf-token")
			session.Set("csrf_token", matches[1])
		}
		break
	}

	_ = dao.SaveRecord(session)
}

// ---------------------------------------------------------------------------
// Session + CSRF injection helper
// ---------------------------------------------------------------------------

// injectSessionAndCSRF injects the active session's cookies, custom headers,
// and CSRF token into a raw HTTP request. CSRF tokens are injected based on
// the request method and content type:
//   - GET: appended as a query parameter
//   - POST with form body: appended as a form field
//   - POST with JSON body: injected into the JSON object
//   - Always added as X-CSRF-Token header as a fallback
func (backend *Backend) injectSessionAndCSRF(rawReq string, method string, body string) string {
	// First, inject cookies and custom headers via the shared helper
	injected, err := backend.injectSessionIntoRequest(rawReq)
	if err == nil {
		rawReq = injected
	}

	// Now inject CSRF token if the active session has one
	session, err := backend.findActiveSession()
	if err != nil {
		return rawReq
	}

	csrfToken, _ := session.Get("csrf_token").(string)
	csrfField, _ := session.Get("csrf_field").(string)
	if csrfToken == "" || csrfField == "" {
		return rawReq
	}

	// Add X-CSRF-Token header as fallback (always)
	if !hasHeader(rawReq, "X-CSRF-Token") {
		rawReq = insertHeaderAfterRequestLine(rawReq, fmt.Sprintf("X-CSRF-Token: %s", csrfToken))
	}

	upperMethod := strings.ToUpper(method)

	if upperMethod == "GET" {
		// Inject CSRF as query parameter
		rawReq = injectCSRFIntoGetQuery(rawReq, csrfField, csrfToken)
	} else if upperMethod == "POST" || upperMethod == "PUT" || upperMethod == "PATCH" {
		rawReq = injectCSRFIntoBody(rawReq, csrfField, csrfToken)
	}

	return rawReq
}

// injectCSRFIntoGetQuery adds the CSRF token as a query parameter on the
// request line of a raw HTTP request.
func injectCSRFIntoGetQuery(rawReq, field, token string) string {
	idx := strings.Index(rawReq, "\r\n")
	if idx < 0 {
		return rawReq
	}
	requestLine := rawReq[:idx]
	rest := rawReq[idx:]

	// Request line format: GET /path?existing=1 HTTP/1.1
	parts := strings.SplitN(requestLine, " ", 3)
	if len(parts) < 3 {
		return rawReq
	}
	pathQuery := parts[1]

	if strings.Contains(pathQuery, "?") {
		pathQuery += "&" + url.QueryEscape(field) + "=" + url.QueryEscape(token)
	} else {
		pathQuery += "?" + url.QueryEscape(field) + "=" + url.QueryEscape(token)
	}

	return parts[0] + " " + pathQuery + " " + parts[2] + rest
}

// injectCSRFIntoBody appends or injects the CSRF token into the request body
// based on content type (form-urlencoded or JSON).
func injectCSRFIntoBody(rawReq, field, token string) string {
	headerSection, body := splitRequestHeadersAndBody(rawReq)

	// Detect content type from headers
	contentType := extractHeaderValue(rawReq, "Content-Type")
	ctLower := strings.ToLower(contentType)

	if strings.Contains(ctLower, "application/json") && body != "" {
		// Try to inject into JSON object
		var jsonObj map[string]any
		if json.Unmarshal([]byte(body), &jsonObj) == nil {
			jsonObj[field] = token
			if newBody, err := json.Marshal(jsonObj); err == nil {
				body = string(newBody)
			}
		}
	} else if strings.Contains(ctLower, "application/x-www-form-urlencoded") || body != "" {
		// Append as form field
		if body != "" {
			body += "&"
		}
		body += url.QueryEscape(field) + "=" + url.QueryEscape(token)
	}

	// Rebuild with updated Content-Length
	headerSection = setHeaderInSection(headerSection, "Content-Length", strconv.Itoa(len(body)))
	return headerSection + "\r\n\r\n" + body
}

// ---------------------------------------------------------------------------
// HTTP parsing helpers
// ---------------------------------------------------------------------------

// splitResponseHeaders splits a raw HTTP response at the header/body boundary.
func splitResponseHeaders(raw string) (string, string) {
	if idx := strings.Index(raw, "\r\n\r\n"); idx >= 0 {
		return raw[:idx], raw[idx+4:]
	}
	if idx := strings.Index(raw, "\n\n"); idx >= 0 {
		return raw[:idx], raw[idx+2:]
	}
	return raw, ""
}

// splitRequestHeadersAndBody splits a raw HTTP request at the \r\n\r\n
// boundary, returning the header section (without trailing \r\n\r\n) and
// the body.
func splitRequestHeadersAndBody(raw string) (string, string) {
	if idx := strings.Index(raw, "\r\n\r\n"); idx >= 0 {
		return raw[:idx], raw[idx+4:]
	}
	if idx := strings.Index(raw, "\n\n"); idx >= 0 {
		return raw[:idx], raw[idx+2:]
	}
	return raw, ""
}

// parseStatusCode extracts the HTTP status code from the first line of a
// raw response (e.g. "HTTP/1.1 200 OK" -> 200).
func parseStatusCode(rawResponse string) int {
	firstLine := rawResponse
	if idx := strings.Index(rawResponse, "\r\n"); idx >= 0 {
		firstLine = rawResponse[:idx]
	} else if idx := strings.Index(rawResponse, "\n"); idx >= 0 {
		firstLine = rawResponse[:idx]
	}

	// "HTTP/1.1 200 OK" -> parts[1] = "200"
	parts := strings.SplitN(firstLine, " ", 3)
	if len(parts) >= 2 {
		code, err := strconv.Atoi(parts[1])
		if err == nil {
			return code
		}
	}
	return 0
}

// parseResponseHeaders extracts all response headers into a map.
// For duplicate header names, the last value wins.
func parseResponseHeaders(rawResponse string) map[string]string {
	headers := make(map[string]string)
	headerSection, _ := splitResponseHeaders(rawResponse)

	lines := strings.Split(headerSection, "\r\n")
	if len(lines) == 1 {
		lines = strings.Split(headerSection, "\n")
	}

	// Skip the status line (first line)
	for i := 1; i < len(lines); i++ {
		colonIdx := strings.Index(lines[i], ":")
		if colonIdx < 0 {
			continue
		}
		name := strings.TrimSpace(lines[i][:colonIdx])
		value := strings.TrimSpace(lines[i][colonIdx+1:])
		headers[name] = value
	}
	return headers
}

// extractHeaderValue returns the value of a named header from a raw HTTP
// message (request or response). The search is case-insensitive.
func extractHeaderValue(raw, headerName string) string {
	headerSection, _ := splitResponseHeaders(raw)
	lowerName := strings.ToLower(headerName) + ":"

	for _, line := range strings.Split(headerSection, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), lowerName) {
			return strings.TrimSpace(line[len(lowerName):])
		}
	}
	// Fallback: try \n splitting
	for _, line := range strings.Split(headerSection, "\n") {
		if strings.HasPrefix(strings.ToLower(line), lowerName) {
			return strings.TrimSpace(line[len(lowerName):])
		}
	}
	return ""
}

// insertHeaderAfterRequestLine inserts a header line immediately after the
// first line (request/status line) of a raw HTTP message.
func insertHeaderAfterRequestLine(raw, headerLine string) string {
	idx := strings.Index(raw, "\r\n")
	if idx < 0 {
		return raw
	}
	return raw[:idx+2] + headerLine + "\r\n" + raw[idx+2:]
}

// setHeaderValue sets or replaces a header in a raw HTTP request string.
// If the header does not exist, it is inserted after the request line.
func setHeaderValue(raw, name, value string) string {
	lowerName := strings.ToLower(name) + ":"

	// Try to find and replace existing header
	lines := strings.Split(raw, "\r\n")
	found := false
	for i, line := range lines {
		if strings.HasPrefix(strings.ToLower(line), lowerName) {
			lines[i] = name + ": " + value
			found = true
			break
		}
	}
	if found {
		return strings.Join(lines, "\r\n")
	}

	// Header not found, insert after first line
	return insertHeaderAfterRequestLine(raw, name+": "+value)
}

// setHeaderInSection sets or replaces a header in a header-only section
// (no body). Used when the header section and body have already been split.
func setHeaderInSection(headerSection, name, value string) string {
	lowerName := strings.ToLower(name) + ":"
	lines := strings.Split(headerSection, "\r\n")
	found := false
	for i, line := range lines {
		if strings.HasPrefix(strings.ToLower(line), lowerName) {
			lines[i] = name + ": " + value
			found = true
			break
		}
	}
	if found {
		return strings.Join(lines, "\r\n")
	}

	// Insert after first line (request/status line)
	if len(lines) >= 1 {
		result := lines[0] + "\r\n" + name + ": " + value
		if len(lines) > 1 {
			result += "\r\n" + strings.Join(lines[1:], "\r\n")
		}
		return result
	}
	return headerSection
}
