package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// Input schema
// ---------------------------------------------------------------------------

type WebSocketArgs struct {
	Action    string `json:"action" jsonschema:"required,enum=listMessages,search,getConnection,listConnections" jsonschema_description:"listMessages: get messages for a connection; search: search message content; getConnection: get upgrade request details; listConnections: list all WebSocket connections"`
	Host      string `json:"host,omitempty" jsonschema_description:"Filter by host"`
	RequestID string `json:"requestId,omitempty" jsonschema_description:"Filter by proxy request ID (connection identifier)"`
	Query     string `json:"query,omitempty" jsonschema_description:"Search string for message payloads"`
	Direction string `json:"direction,omitempty" jsonschema_description:"Filter by direction: send or recv"`
	Limit     int    `json:"limit,omitempty" jsonschema_description:"Max results (default 100)"`
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func (backend *Backend) websocketHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args WebSocketArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "listMessages":
		return backend.wsListMessagesHandler(args)
	case "search":
		return backend.wsSearchHandler(args)
	case "getConnection":
		return backend.wsGetConnectionHandler(args)
	case "listConnections":
		return backend.wsListConnectionsHandler(args)
	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: listMessages, search, getConnection, listConnections"), nil
	}
}

// ---------------------------------------------------------------------------
// listMessages — retrieve WebSocket messages, optionally filtered
// ---------------------------------------------------------------------------

func (backend *Backend) wsListMessagesHandler(args WebSocketArgs) (*mcp.CallToolResult, error) {
	limit := clampWSLimit(args.Limit, 100, 500)

	var conditions []string
	var queryArgs []any

	if args.Host != "" {
		conditions = append(conditions, "host LIKE ?")
		queryArgs = append(queryArgs, "%"+args.Host+"%")
	}
	if args.RequestID != "" {
		conditions = append(conditions, "proxy_id = ?")
		queryArgs = append(queryArgs, args.RequestID)
	}
	if args.Direction != "" {
		conditions = append(conditions, "direction = ?")
		queryArgs = append(queryArgs, args.Direction)
	}

	where := "1=1"
	if len(conditions) > 0 {
		where = strings.Join(conditions, " AND ")
	}

	records, err := backend.DB.FindRecordsSorted("_websockets", where, "timestamp DESC", limit, 0, queryArgs...)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to query websocket messages: %v", err)), nil
	}

	var messages []map[string]any
	for _, r := range records {
		msg := map[string]any{
			"id":        r.Id,
			"index":     r.GetInt("index"),
			"host":      r.GetString("host"),
			"path":      r.GetString("path"),
			"url":       r.GetString("url"),
			"direction": r.GetString("direction"),
			"type":      r.GetString("type"),
			"isBinary":  r.GetBool("is_binary"),
			"length":    r.GetInt("length"),
			"timestamp": r.GetString("timestamp"),
			"proxyId":   r.GetString("proxy_id"),
		}
		payload := r.GetString("payload")
		if len(payload) > 2000 {
			msg["payload"] = payload[:2000] + "...[truncated]"
			msg["truncated"] = true
		} else {
			msg["payload"] = payload
		}
		messages = append(messages, msg)
	}

	return mcpJSONResult(map[string]any{
		"messages": messages,
		"count":    len(messages),
	})
}

// ---------------------------------------------------------------------------
// search — full-text search across WebSocket message payloads
// ---------------------------------------------------------------------------

func (backend *Backend) wsSearchHandler(args WebSocketArgs) (*mcp.CallToolResult, error) {
	if args.Query == "" {
		return mcp.NewToolResultError("query is required for search"), nil
	}

	limit := clampWSLimit(args.Limit, 100, 500)

	var conditions []string
	var queryArgs []any

	conditions = append(conditions, "payload LIKE ?")
	queryArgs = append(queryArgs, "%"+args.Query+"%")

	if args.Host != "" {
		conditions = append(conditions, "host LIKE ?")
		queryArgs = append(queryArgs, "%"+args.Host+"%")
	}
	if args.Direction != "" {
		conditions = append(conditions, "direction = ?")
		queryArgs = append(queryArgs, args.Direction)
	}

	where := strings.Join(conditions, " AND ")
	records, err := backend.DB.FindRecordsSorted("_websockets", where, "timestamp DESC", limit, 0, queryArgs...)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", err)), nil
	}

	var results []map[string]any
	for _, r := range records {
		payload := r.GetString("payload")
		matchCtx := extractWSMatchContext(payload, args.Query, 100)

		results = append(results, map[string]any{
			"id":        r.Id,
			"index":     r.GetInt("index"),
			"host":      r.GetString("host"),
			"path":      r.GetString("path"),
			"direction": r.GetString("direction"),
			"type":      r.GetString("type"),
			"length":    r.GetInt("length"),
			"match":     matchCtx,
			"proxyId":   r.GetString("proxy_id"),
		})
	}

	return mcpJSONResult(map[string]any{
		"results": results,
		"count":   len(results),
		"query":   args.Query,
	})
}

// ---------------------------------------------------------------------------
// getConnection — retrieve the HTTP upgrade request for a WS connection
// ---------------------------------------------------------------------------

func (backend *Backend) wsGetConnectionHandler(args WebSocketArgs) (*mcp.CallToolResult, error) {
	if args.RequestID == "" {
		return mcp.NewToolResultError("requestId is required for getConnection"), nil
	}

	// Find the HTTP upgrade request in _data
	r, err := backend.DB.FindRecordById("_data", args.RequestID)
	if err != nil || r == nil {
		return mcp.NewToolResultError(fmt.Sprintf("connection %s not found in traffic data", args.RequestID)), nil
	}

	// Get raw request/response
	var rawReq, rawResp string
	rawRecord, rawErr := backend.DB.FindRecordById("_raw", args.RequestID)
	if rawErr == nil && rawRecord != nil {
		rawReq = rawRecord.GetString("request")
		rawResp = rawRecord.GetString("response")
	}

	// Count messages for this connection
	wsRecords, _ := backend.DB.FindRecords("_websockets", "proxy_id = ?", args.RequestID)

	return mcpJSONResult(map[string]any{
		"requestId":    args.RequestID,
		"host":         r.GetString("host"),
		"path":         r.GetString("path"),
		"status":       r.GetInt("status"),
		"rawRequest":   rawReq,
		"rawResponse":  rawResp,
		"messageCount": len(wsRecords),
	})
}

// ---------------------------------------------------------------------------
// listConnections — aggregate distinct WebSocket connections
// ---------------------------------------------------------------------------

func (backend *Backend) wsListConnectionsHandler(args WebSocketArgs) (*mcp.CallToolResult, error) {
	limit := clampWSLimit(args.Limit, 50, 200)

	rows, err := backend.DB.Query(`
		SELECT proxy_id, host, path, url,
		       MIN(timestamp) AS first_msg,
		       MAX(timestamp) AS last_msg,
		       COUNT(*) AS msg_count
		FROM _websockets
		GROUP BY proxy_id
		ORDER BY first_msg DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("query failed: %v", err)), nil
	}
	defer rows.Close()

	var connections []map[string]any
	for rows.Next() {
		var proxyID, host, path, url, firstMsg, lastMsg string
		var msgCount int
		if err := rows.Scan(&proxyID, &host, &path, &url, &firstMsg, &lastMsg, &msgCount); err != nil {
			continue
		}
		connections = append(connections, map[string]any{
			"proxyId":      proxyID,
			"host":         host,
			"path":         path,
			"url":          url,
			"firstMessage": firstMsg,
			"lastMessage":  lastMsg,
			"messageCount": msgCount,
		})
	}

	return mcpJSONResult(map[string]any{
		"connections": connections,
		"count":       len(connections),
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// clampWSLimit returns limit clamped to [1, max], using defaultVal when <= 0.
func clampWSLimit(limit, defaultVal, max int) int {
	if limit <= 0 {
		return defaultVal
	}
	if limit > max {
		return max
	}
	return limit
}

// extractWSMatchContext returns a substring centered on the first occurrence of
// query within text, with contextLen characters of surrounding context.
func extractWSMatchContext(text, query string, contextLen int) string {
	lower := strings.ToLower(text)
	queryLower := strings.ToLower(query)
	idx := strings.Index(lower, queryLower)
	if idx < 0 {
		return ""
	}
	start := idx - contextLen
	if start < 0 {
		start = 0
	}
	end := idx + len(query) + contextLen
	if end > len(text) {
		end = len(text)
	}
	result := text[start:end]
	if start > 0 {
		result = "..." + result
	}
	if end < len(text) {
		result = result + "..."
	}
	return result
}
