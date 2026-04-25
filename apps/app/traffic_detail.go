package app

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

// TrafficDetailResponse is returned by the unified traffic detail endpoint.
type TrafficDetailResponse struct {
	ID       string `json:"id"`
	Request  string `json:"request"`
	Response string `json:"response"`
	Source   string `json:"source"` // "raw", "json", or "none"
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

		// 1. Try _req / _resp collections first (proxy-generated traffic stores raw here)
		var rawReq, rawResp string
		reqRec, reqErr := backend.DB.FindRecordById("_req", id)
		if reqErr == nil && reqRec != nil {
			rawReq = reqRec.GetString("raw")
		}
		respRec, respErr := backend.DB.FindRecordById("_resp", id)
		if respErr == nil && respRec != nil {
			rawResp = respRec.GetString("raw")
		}

		if rawReq != "" || rawResp != "" {
			return c.JSON(http.StatusOK, TrafficDetailResponse{
				ID:       id,
				Request:  rawReq,
				Response: rawResp,
				Source:   "raw",
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
				ID:       id,
				Request:  directReq,
				Response: directResp,
				Source:   "raw",
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
