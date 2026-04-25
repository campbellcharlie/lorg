package app

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
)

// TrafficDetailResponse is returned by the unified traffic detail endpoint.
type TrafficDetailResponse struct {
	ID        string `json:"id"`
	Request   string `json:"request"`
	Response  string `json:"response"`
	Source    string `json:"source"` // "raw", "json", or "none"
	ElapsedMs int64  `json:"elapsed_ms,omitempty"`
}

// TrafficDetail registers GET /api/traffic/:id/detail and
// GET /api/traffic/:id/body (raw body with original content-type, used by
// the UI for image preview / hex dumps without re-parsing the raw HTTP).
func (backend *Backend) TrafficDetail(e *echo.Echo) {
	// /api/traffic/:id/body?part=response  -> raw response body bytes
	// /api/traffic/:id/body?part=request   -> raw request body bytes
	// Sets Content-Type from the captured headers so an <img src> tag
	// just works for image/* responses.
	e.GET("/api/traffic/:id/body", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}
		id := c.Param("id")
		part := c.QueryParam("part")
		if part == "" {
			part = "response"
		}

		var rawTable, headerSrc string
		if part == "request" {
			rawTable = "_req"
			headerSrc = "request"
		} else {
			rawTable = "_resp"
			headerSrc = "response"
		}
		_ = headerSrc

		rec, err := backend.DB.FindRecordById(rawTable, id)
		if err != nil || rec == nil {
			return c.NoContent(http.StatusNotFound)
		}
		raw := rec.GetString("raw")
		if raw == "" {
			return c.NoContent(http.StatusNotFound)
		}

		// Split headers / body. Use \r\n\r\n then \n\n as fallback.
		headers, body := splitHTTPBody(raw)
		ct := extractContentType(headers)
		if ct == "" {
			ct = "application/octet-stream"
		}
		c.Response().Header().Set("Content-Type", ct)
		c.Response().Header().Set("X-Content-Type-Options", "nosniff")
		return c.Blob(http.StatusOK, ct, []byte(body))
	})

	e.GET("/api/traffic/:id/detail", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}

		id := c.Param("id")
		if id == "" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "id is required"})
		}

		// If an active projectDB has this id (numeric request_id from
		// http_traffic), serve from there — that's where the listed
		// traffic actually came from when the user switched DBs.
		if resp, ok := tryServeProjectDBDetail(id); ok {
			return c.JSON(http.StatusOK, resp)
		}

		// 1. Try _req / _resp collections first (proxy-generated traffic stores raw here)
		var rawReq, rawResp string
		var reqCreated, respCreated string
		reqRec, reqErr := backend.DB.FindRecordById("_req", id)
		if reqErr == nil && reqRec != nil {
			rawReq = reqRec.GetString("raw")
			reqCreated = reqRec.Created
		}
		respRec, respErr := backend.DB.FindRecordById("_resp", id)
		if respErr == nil && respRec != nil {
			rawResp = respRec.GetString("raw")
			respCreated = respRec.Created
		}

		if rawReq != "" || rawResp != "" {
			return c.JSON(http.StatusOK, TrafficDetailResponse{
				ID:        id,
				Request:   rawReq,
				Response:  rawResp,
				Source:    "raw",
				ElapsedMs: computeElapsedMs(reqCreated, respCreated),
			})
		}

		// 2. Fallback: reconstruct from _data record's req_json / resp_json
		dataRec, dataErr := backend.DB.FindRecordById("_data", id)
		if dataErr != nil {
			log.Printf("[TrafficDetail] Record %s not found in _req/_resp or _data: %v", id, dataErr)
			return c.JSON(http.StatusOK, TrafficDetailResponse{
				ID:       id,
				Request:  "",
				Response: "",
				Source:   "none",
			})
		}

		// Also check if raw req/resp are stored directly on _data
		directReq := dataRec.GetString("req")
		directResp := dataRec.GetString("resp")
		if directReq != "" || directResp != "" {
			return c.JSON(http.StatusOK, TrafficDetailResponse{
				ID:        id,
				Request:   directReq,
				Response:  directResp,
				Source:    "raw",
				ElapsedMs: computeElapsedMs(dataRec.Created, dataRec.Updated),
			})
		}

		// 3. Last resort: reconstruct from JSON fields
		reqJSON := dataRec.Get("req_json")
		respJSON := dataRec.Get("resp_json")

		reconstructedReq := reconstructFromJSON(reqJSON, dataRec.GetString("host"), true)
		reconstructedResp := reconstructFromJSON(respJSON, "", false)

		return c.JSON(http.StatusOK, TrafficDetailResponse{
			ID:       id,
			Request:  reconstructedReq,
			Response: reconstructedResp,
			Source:   "json",
		})
	})
}

// computeElapsedMs returns the round-trip in milliseconds between the
// captured-request timestamp and captured-response timestamp. Returns 0
// when either side is missing or unparseable so the JSON marshaller
// omits the field (omitempty on the struct).
func computeElapsedMs(reqTS, respTS string) int64 {
	if reqTS == "" || respTS == "" {
		return 0
	}
	const layout = "2006-01-02 15:04:05.000Z"
	t1, err1 := time.Parse(layout, reqTS)
	t2, err2 := time.Parse(layout, respTS)
	if err1 != nil || err2 != nil {
		return 0
	}
	d := t2.Sub(t1).Milliseconds()
	if d < 0 {
		return 0
	}
	return d
}

// tryServeProjectDBDetail reconstructs a raw HTTP request/response pair
// for a numeric http_traffic.request_id from the active projectDB. Returns
// (nil, false) when there's no projectDB, the id isn't numeric, or the
// row doesn't exist.
func tryServeProjectDBDetail(id string) (*TrafficDetailResponse, bool) {
	if projectDB == nil {
		return nil, false
	}
	projectDB.mu.Lock()
	db := projectDB.db
	ready := projectDB.ready
	projectDB.mu.Unlock()
	if db == nil || !ready {
		return nil, false
	}

	// http_traffic.request_id is INTEGER, but the UI passes us a string.
	// Reject anything non-numeric so we don't quote-inject.
	for _, r := range id {
		if r < '0' || r > '9' {
			return nil, false
		}
	}

	var (
		method, host, path, query string
		protocol                  string
		port                      int
		status                    int
		respLen                   int64
		mime, ts                  string
	)
	err := db.QueryRow(`SELECT COALESCE(method,''), COALESCE(host,''), COALESCE(path,''),
		COALESCE(query,''), COALESCE(protocol,''), COALESCE(port,0),
		COALESCE(status_code,0), COALESCE(response_length,0),
		COALESCE(mime_type,''), COALESCE(timestamp,'')
		FROM http_traffic WHERE request_id = ?`, id).Scan(
		&method, &host, &path, &query, &protocol, &port, &status, &respLen, &mime, &ts,
	)
	if err != nil {
		return nil, false
	}

	var reqHeaders, respHeaders string
	var reqBody, respBody []byte
	_ = db.QueryRow(`SELECT COALESCE(request_headers,''), COALESCE(request_body, x''),
		COALESCE(response_headers,''), COALESCE(response_body, x'')
		FROM http_messages WHERE request_id = ?`, id).Scan(
		&reqHeaders, &reqBody, &respHeaders, &respBody,
	)

	// http_messages.request_headers usually already includes the request
	// line. Only synthesize a "METHOD path HTTP/1.1" prefix if the stored
	// headers don't begin with one.
	pathPart := path
	if query != "" {
		pathPart = path + "?" + query
	}
	var rawReq string
	if reqHeaders != "" {
		trimmed := strings.TrimLeft(reqHeaders, "\r\n")
		firstLine := trimmed
		if i := strings.IndexAny(trimmed, "\r\n"); i >= 0 {
			firstLine = trimmed[:i]
		}
		if looksLikeRequestLine(firstLine) {
			rawReq = strings.TrimRight(reqHeaders, "\r\n") + "\r\n\r\n"
		} else {
			rawReq = method + " " + pathPart + " HTTP/1.1\r\n" +
				strings.TrimRight(reqHeaders, "\r\n") + "\r\n\r\n"
		}
	} else {
		rawReq = method + " " + pathPart + " HTTP/1.1\r\nHost: " + host + "\r\n\r\n"
	}
	if len(reqBody) > 0 {
		rawReq += string(reqBody)
	}

	// Same logic for the response: status line may already be in
	// response_headers.
	statusText := http.StatusText(status)
	statusLine := "HTTP/1.1 " + fmt.Sprintf("%d", status) + " " + statusText
	var rawResp string
	if respHeaders != "" {
		trimmed := strings.TrimLeft(respHeaders, "\r\n")
		firstLine := trimmed
		if i := strings.IndexAny(trimmed, "\r\n"); i >= 0 {
			firstLine = trimmed[:i]
		}
		if strings.HasPrefix(firstLine, "HTTP/") {
			rawResp = strings.TrimRight(respHeaders, "\r\n") + "\r\n\r\n"
		} else {
			rawResp = statusLine + "\r\n" + strings.TrimRight(respHeaders, "\r\n") + "\r\n\r\n"
		}
	} else {
		rawResp = statusLine + "\r\n\r\n"
	}
	if len(respBody) > 0 {
		rawResp += string(respBody)
	}

	_ = mime
	_ = respLen

	return &TrafficDetailResponse{
		ID:       id,
		Request:  rawReq,
		Response: rawResp,
		Source:   "projectDB",
	}, true
}

// looksLikeRequestLine returns true when s starts with "<METHOD> <path> HTTP/".
// Cheap shape check — we don't need to validate the full grammar.
func looksLikeRequestLine(s string) bool {
	parts := strings.SplitN(s, " ", 3)
	if len(parts) < 3 {
		return false
	}
	return strings.HasPrefix(parts[2], "HTTP/")
}

// splitHTTPBody splits a raw HTTP message into headers + body. Uses
// \r\n\r\n then \n\n as fallback. Mirrors splitHTTPRaw in mcp_project.go
// but lives here to avoid an import cycle in tests.
func splitHTTPBody(raw string) (headers, body string) {
	if raw == "" {
		return "", ""
	}
	if idx := strings.Index(raw, "\r\n\r\n"); idx != -1 {
		return raw[:idx], raw[idx+4:]
	}
	if idx := strings.Index(raw, "\n\n"); idx != -1 {
		return raw[:idx], raw[idx+2:]
	}
	return raw, ""
}

// extractContentType pulls the Content-Type header value out of a raw
// HTTP header block. Case-insensitive header name match.
func extractContentType(headers string) string {
	for _, line := range strings.Split(headers, "\n") {
		line = strings.TrimRight(line, "\r")
		if i := strings.IndexByte(line, ':'); i > 0 {
			name := strings.TrimSpace(line[:i])
			if strings.EqualFold(name, "Content-Type") {
				return strings.TrimSpace(line[i+1:])
			}
		}
	}
	return ""
}

// reconstructFromJSON attempts to build a raw HTTP string from parsed JSON data.
func reconstructFromJSON(jsonData interface{}, host string, isRequest bool) string {
	m, ok := jsonData.(map[string]interface{})
	if !ok || m == nil {
		return ""
	}

	var sb strings.Builder

	if isRequest {
		method, _ := m["method"].(string)
		path, _ := m["path"].(string)
		if path == "" {
			path, _ = m["url"].(string)
		}
		if method == "" {
			method = "GET"
		}
		if path == "" {
			path = "/"
		}
		sb.WriteString(fmt.Sprintf("%s %s HTTP/1.1\r\n", method, path))
		if host != "" {
			cleanHost := strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
			sb.WriteString(fmt.Sprintf("Host: %s\r\n", cleanHost))
		}
	} else {
		status, _ := m["status"].(float64)
		statusText, _ := m["status_text"].(string)
		if status == 0 {
			status = 200
		}
		if statusText == "" {
			statusText = http.StatusText(int(status))
		}
		sb.WriteString(fmt.Sprintf("HTTP/1.1 %d %s\r\n", int(status), statusText))
	}

	// Write headers if present
	if headers, ok := m["headers"].(map[string]interface{}); ok {
		for k, v := range headers {
			sb.WriteString(fmt.Sprintf("%s: %v\r\n", k, v))
		}
	}

	sb.WriteString("\r\n")

	// Write body if present
	if body, ok := m["body"].(string); ok && body != "" {
		sb.WriteString(body)
	}

	return sb.String()
}
