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

// TrafficDetail registers GET /api/traffic/:id/detail
func (backend *Backend) TrafficDetail(e *echo.Echo) {
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
