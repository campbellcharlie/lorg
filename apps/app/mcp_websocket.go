package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/pocketbase/dbx"
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
	dao := backend.App.Dao()
	limit := clampWSLimit(args.Limit, 100, 500)

	var filters []string
	params := dbx.Params{}

	if args.Host != "" {
		filters = append(filters, "host ~ {:host}")
		params["host"] = args.Host
	}
	if args.RequestID != "" {
		filters = append(filters, "proxy_id = {:pid}")
		params["pid"] = args.RequestID
	}
	if args.Direction != "" {
		filters = append(filters, "direction = {:dir}")
		params["dir"] = args.Direction
	}

	filter := "id != ''"
	if len(filters) > 0 {
		filter = strings.Join(filters, " && ")
	}

	records, err := dao.FindRecordsByFilter("_websockets", filter, "-timestamp", limit, 0, params)
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

	dao := backend.App.Dao()
	limit := clampWSLimit(args.Limit, 100, 500)

	var filters []string
	params := dbx.Params{}

	filters = append(filters, "payload ~ {:q}")
	params["q"] = args.Query

	if args.Host != "" {
		filters = append(filters, "host ~ {:host}")
		params["host"] = args.Host
	}
	if args.Direction != "" {
		filters = append(filters, "direction = {:dir}")
		params["dir"] = args.Direction
	}

	filter := strings.Join(filters, " && ")
	records, err := dao.FindRecordsByFilter("_websockets", filter, "-timestamp", limit, 0, params)
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

	dao := backend.App.Dao()

	// Find the HTTP upgrade request in _data
	dataRecords, err := dao.FindRecordsByFilter("_data", "id = {:id}", "", 1, 0, dbx.Params{"id": args.RequestID})
	if err != nil || len(dataRecords) == 0 {
		return mcp.NewToolResultError(fmt.Sprintf("connection %s not found in traffic data", args.RequestID)), nil
	}

	r := dataRecords[0]

	// Get raw request/response
	var rawReq, rawResp string
	rawRecords, rawErr := dao.FindRecordsByFilter("_raw", "id = {:id}", "", 1, 0, dbx.Params{"id": args.RequestID})
	if rawErr == nil && len(rawRecords) > 0 {
		rawReq = rawRecords[0].GetString("request")
		rawResp = rawRecords[0].GetString("response")
	}

	// Count messages for this connection
	wsRecords, _ := dao.FindRecordsByFilter("_websockets", "proxy_id = {:pid}", "", 0, 0, dbx.Params{"pid": args.RequestID})

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
	dao := backend.App.Dao()
	limit := clampWSLimit(args.Limit, 50, 200)

	query := dao.DB().NewQuery(`
		SELECT proxy_id, host, path, url,
		       MIN(timestamp) AS first_msg,
		       MAX(timestamp) AS last_msg,
		       COUNT(*) AS msg_count
		FROM _websockets
		GROUP BY proxy_id
		ORDER BY first_msg DESC
		LIMIT {:limit}
	`).Bind(dbx.Params{"limit": limit})

	var connections []map[string]any
	rows, err := query.Rows()
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("query failed: %v", err)), nil
	}
	defer rows.Close()

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
