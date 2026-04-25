package app

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// SSE Client State
// ---------------------------------------------------------------------------

type sseEvent struct {
	ID        string    `json:"id,omitempty"`
	Type      string    `json:"type"`
	Data      string    `json:"data"`
	Timestamp time.Time `json:"timestamp"`
}

type sseConnection struct {
	URL       string
	Events    []sseEvent
	Connected bool
	cancel    context.CancelFunc
	mu        sync.Mutex
}

var (
	sseConnections   = make(map[string]*sseConnection) // key = URL
	sseConnectionsMu sync.Mutex
)

// ---------------------------------------------------------------------------
// Input schema
// ---------------------------------------------------------------------------

type SSEClientArgs struct {
	Action  string            `json:"action" jsonschema:"required,enum=connect,listEvents,disconnect,listConnections" jsonschema_description:"connect: open SSE stream; listEvents: get captured events; disconnect: close stream; listConnections: show all active SSE connections"`
	URL     string            `json:"url,omitempty" jsonschema_description:"SSE endpoint URL (required for connect/listEvents/disconnect)"`
	Headers map[string]string `json:"headers,omitempty" jsonschema_description:"Custom headers for the SSE connection"`
	Limit   int               `json:"limit,omitempty" jsonschema_description:"Max events to return (default 100)"`
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func (backend *Backend) sseClientHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args SSEClientArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "connect":
		return backend.sseConnectHandler(args)
	case "listEvents":
		return backend.sseListEventsHandler(args)
	case "disconnect":
		return backend.sseDisconnectHandler(args)
	case "listConnections":
		return backend.sseListConnectionsHandler()
	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: connect, listEvents, disconnect, listConnections"), nil
	}
}

func (backend *Backend) sseConnectHandler(args SSEClientArgs) (*mcp.CallToolResult, error) {
	if args.URL == "" {
		return mcp.NewToolResultError("url is required for connect"), nil
	}

	sseConnectionsMu.Lock()
	if conn, exists := sseConnections[args.URL]; exists && conn.Connected {
		sseConnectionsMu.Unlock()
		return mcp.NewToolResultError("already connected to " + args.URL), nil
	}
	sseConnectionsMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())

	conn := &sseConnection{
		URL:       args.URL,
		Connected: true,
		cancel:    cancel,
	}

	sseConnectionsMu.Lock()
	sseConnections[args.URL] = conn
	sseConnectionsMu.Unlock()

	// Start background SSE reader
	go func() {
		defer func() {
			conn.mu.Lock()
			conn.Connected = false
			conn.mu.Unlock()
		}()

		req, err := http.NewRequestWithContext(ctx, "GET", args.URL, nil)
		if err != nil {
			log.Printf("[SSE] Failed to create request: %v", err)
			return
		}
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Cache-Control", "no-cache")
		for k, v := range args.Headers {
			req.Header.Set(k, v)
		}

		client := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
			Timeout: 0, // no timeout for SSE
		}

		resp, err := client.Do(req)
		if err != nil {
			log.Printf("[SSE] Connection failed: %v", err)
			return
		}
		defer resp.Body.Close()

		log.Printf("[SSE] Connected to %s (status: %d)", args.URL, resp.StatusCode)

		scanner := bufio.NewScanner(resp.Body)
		var currentEvent sseEvent
		var dataLines []string

		for scanner.Scan() {
			line := scanner.Text()

			if line == "" {
				// Empty line = event boundary
				if len(dataLines) > 0 {
					currentEvent.Data = strings.Join(dataLines, "\n")
					currentEvent.Timestamp = time.Now()
					if currentEvent.Type == "" {
						currentEvent.Type = "message"
					}
					conn.mu.Lock()
					conn.Events = append(conn.Events, currentEvent)
					conn.mu.Unlock()
					log.Printf("[SSE] Event received: type=%s id=%s len=%d", currentEvent.Type, currentEvent.ID, len(currentEvent.Data))
				}
				currentEvent = sseEvent{}
				dataLines = nil
				continue
			}

			if strings.HasPrefix(line, "data:") {
				data := strings.TrimPrefix(line, "data:")
				data = strings.TrimPrefix(data, " ")
				dataLines = append(dataLines, data)
			} else if strings.HasPrefix(line, "event:") {
				currentEvent.Type = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			} else if strings.HasPrefix(line, "id:") {
				currentEvent.ID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
			}
			// Ignore retry: and comments (:)
		}

		if scanErr := scanner.Err(); scanErr != nil && scanErr != io.EOF && ctx.Err() == nil {
			log.Printf("[SSE] Stream error: %v", scanErr)
		}
	}()

	return mcpJSONResult(map[string]any{
		"success":   true,
		"url":       args.URL,
		"connected": true,
		"message":   "SSE stream connected. Use listEvents to retrieve captured events.",
	})
}

func (backend *Backend) sseListEventsHandler(args SSEClientArgs) (*mcp.CallToolResult, error) {
	url, conn, err := resolveSSEConnection(args.URL)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	args.URL = url
	_ = conn // keep handle for the read below

	limit := args.Limit
	if limit <= 0 {
		limit = 100
	}

	conn.mu.Lock()
	events := make([]sseEvent, len(conn.Events))
	copy(events, conn.Events)
	connected := conn.Connected
	total := len(conn.Events)
	conn.mu.Unlock()

	// Return most recent events up to limit
	if len(events) > limit {
		events = events[len(events)-limit:]
	}

	return mcpJSONResult(map[string]any{
		"url":       args.URL,
		"connected": connected,
		"events":    events,
		"count":     len(events),
		"total":     total,
	})
}

func (backend *Backend) sseDisconnectHandler(args SSEClientArgs) (*mcp.CallToolResult, error) {
	url, _, err := resolveSSEConnection(args.URL)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	sseConnectionsMu.Lock()
	conn := sseConnections[url]
	conn.cancel()
	delete(sseConnections, url)
	sseConnectionsMu.Unlock()

	return mcpJSONResult(map[string]any{
		"success":     true,
		"url":         url,
		"eventsTotal": len(conn.Events),
	})
}

// resolveSSEConnection picks an active SSE connection by URL. If no URL is
// supplied AND there's exactly one tracked connection, it auto-resolves to
// that one — saves the caller from echoing back the URL on every listEvents
// or disconnect call when only one stream is in flight.
func resolveSSEConnection(url string) (string, *sseConnection, error) {
	sseConnectionsMu.Lock()
	defer sseConnectionsMu.Unlock()

	if url != "" {
		conn, ok := sseConnections[url]
		if !ok {
			return "", nil, fmt.Errorf("no connection found for %s", url)
		}
		return url, conn, nil
	}

	switch len(sseConnections) {
	case 0:
		return "", nil, fmt.Errorf("no SSE connections — call action:connect with url first")
	case 1:
		for u, c := range sseConnections {
			return u, c, nil
		}
	}

	urls := make([]string, 0, len(sseConnections))
	for u := range sseConnections {
		urls = append(urls, u)
	}
	return "", nil, fmt.Errorf("multiple connections (%v); pass url to disambiguate", urls)
}

func (backend *Backend) sseListConnectionsHandler() (*mcp.CallToolResult, error) {
	sseConnectionsMu.Lock()
	defer sseConnectionsMu.Unlock()

	conns := make([]map[string]any, 0, len(sseConnections))
	for url, conn := range sseConnections {
		conn.mu.Lock()
		conns = append(conns, map[string]any{
			"url":        url,
			"connected":  conn.Connected,
			"eventCount": len(conn.Events),
		})
		conn.mu.Unlock()
	}

	return mcpJSONResult(map[string]any{
		"connections": conns,
		"count":       len(conns),
	})
}
