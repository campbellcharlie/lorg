package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"golang.org/x/net/http2"
)

// ---------------------------------------------------------------------------
// Input schemas
// ---------------------------------------------------------------------------

type SendParallelArgs struct {
	Host    string `json:"host" jsonschema:"required" jsonschema_description:"Target hostname"`
	Port    int    `json:"port" jsonschema:"required" jsonschema_description:"Target port"`
	TLS     bool   `json:"tls" jsonschema:"required" jsonschema_description:"Use TLS"`
	Request string `json:"request" jsonschema:"required" jsonschema_description:"Raw HTTP request to send"`
	Count   int    `json:"count" jsonschema:"required" jsonschema_description:"Number of identical requests to send simultaneously (max 50)"`
	Note    string `json:"note,omitempty" jsonschema_description:"Note to attach"`
}

type SendParallelDifferentArgs struct {
	Host     string   `json:"host" jsonschema:"required" jsonschema_description:"Target hostname"`
	Port     int      `json:"port" jsonschema:"required" jsonschema_description:"Target port"`
	TLS      bool     `json:"tls" jsonschema:"required" jsonschema_description:"Use TLS"`
	Requests []string `json:"requests" jsonschema:"required" jsonschema_description:"Different raw HTTP requests to send simultaneously"`
	Note     string   `json:"note,omitempty" jsonschema_description:"Note to attach"`
}

type SendParallelH2Args struct {
	Host     string   `json:"host" jsonschema:"required" jsonschema_description:"Target hostname"`
	Port     int      `json:"port" jsonschema:"required" jsonschema_description:"Target port"`
	Requests []string `json:"requests" jsonschema:"required" jsonschema_description:"Raw HTTP requests to multiplex on single H2 connection"`
	Note     string   `json:"note,omitempty" jsonschema_description:"Note to attach"`
}

type LastByteSyncArgs struct {
	Host    string `json:"host" jsonschema:"required" jsonschema_description:"Target hostname"`
	Port    int    `json:"port" jsonschema:"required" jsonschema_description:"Target port"`
	TLS     bool   `json:"tls" jsonschema:"required" jsonschema_description:"Use TLS"`
	Request string `json:"request" jsonschema:"required" jsonschema_description:"Raw HTTP request"`
	Count   int    `json:"count" jsonschema:"required" jsonschema_description:"Number of connections (max 50)"`
	Note    string `json:"note,omitempty" jsonschema_description:"Note to attach"`
}

// ---------------------------------------------------------------------------
// Shared types and helpers
// ---------------------------------------------------------------------------

type raceResult struct {
	Index      int     `json:"index"`
	StatusLine string  `json:"statusLine"`
	BodyLength int     `json:"bodyLength"`
	TimeMs     float64 `json:"timeMs"`
	Error      string  `json:"error,omitempty"`
}

func dialTarget(host string, port int, useTLS bool) (net.Conn, error) {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	timeout := 10 * time.Second

	if useTLS {
		return tls.DialWithDialer(&net.Dialer{Timeout: timeout}, "tcp", addr, &tls.Config{
			InsecureSkipVerify: true,
			ServerName:         host,
		})
	}
	return net.DialTimeout("tcp", addr, timeout)
}

func sendAndRead(conn net.Conn, request []byte) (string, int, error) {
	conn.SetDeadline(time.Now().Add(30 * time.Second))

	if _, err := conn.Write(request); err != nil {
		return "", 0, fmt.Errorf("write error: %v", err)
	}

	buf := make([]byte, 64*1024)
	var response strings.Builder
	totalRead := 0
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			response.Write(buf[:n])
			totalRead += n
		}
		if err != nil {
			break
		}
		if totalRead > 256*1024 {
			break
		}
	}

	resp := response.String()
	statusLine := ""
	if idx := strings.Index(resp, "\r\n"); idx > 0 {
		statusLine = resp[:idx]
	} else if idx := strings.Index(resp, "\n"); idx > 0 {
		statusLine = resp[:idx]
	}

	return statusLine, totalRead, nil
}

func sendAndReadResponse(conn net.Conn) (string, int, error) {
	buf := make([]byte, 64*1024)
	var response strings.Builder
	totalRead := 0
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			response.Write(buf[:n])
			totalRead += n
		}
		if err != nil {
			break
		}
		if totalRead > 256*1024 {
			break
		}
	}

	resp := response.String()
	statusLine := ""
	if idx := strings.Index(resp, "\r\n"); idx > 0 {
		statusLine = resp[:idx]
	} else if idx := strings.Index(resp, "\n"); idx > 0 {
		statusLine = resp[:idx]
	}

	return statusLine, totalRead, nil
}

func computeTimingStats(results []raceResult) map[string]any {
	var minMs, maxMs float64
	first := true
	for _, r := range results {
		if r.Error == "" {
			if first {
				minMs = r.TimeMs
				maxMs = r.TimeMs
				first = false
			} else {
				if r.TimeMs < minMs {
					minMs = r.TimeMs
				}
				if r.TimeMs > maxMs {
					maxMs = r.TimeMs
				}
			}
		}
	}
	return map[string]any{"minMs": minMs, "maxMs": maxMs, "spreadMs": maxMs - minMs}
}

// normalizeCRLF ensures proper \r\n line endings for HTTP compliance.
func normalizeCRLF(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

// ---------------------------------------------------------------------------
// Tool handlers
// ---------------------------------------------------------------------------

func (backend *Backend) sendParallelHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args SendParallelArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	if args.Count < 1 {
		return mcp.NewToolResultError("count must be at least 1"), nil
	}
	if args.Count > 50 {
		args.Count = 50
	}

	args.Request = normalizeCRLF(args.Request)

	results := make([]raceResult, args.Count)
	barrier := make(chan struct{})
	var wg sync.WaitGroup
	var readyCount sync.WaitGroup

	for i := 0; i < args.Count; i++ {
		wg.Add(1)
		readyCount.Add(1)
		go func(idx int) {
			defer wg.Done()

			conn, err := dialTarget(args.Host, args.Port, args.TLS)
			if err != nil {
				results[idx] = raceResult{Index: idx, Error: err.Error()}
				readyCount.Done()
				return
			}
			defer conn.Close()

			readyCount.Done()
			<-barrier

			start := time.Now()
			statusLine, bodyLen, err := sendAndRead(conn, []byte(args.Request))
			elapsed := time.Since(start)

			r := raceResult{
				Index:      idx,
				StatusLine: statusLine,
				BodyLength: bodyLen,
				TimeMs:     float64(elapsed.Microseconds()) / 1000.0,
			}
			if err != nil {
				r.Error = err.Error()
			}
			results[idx] = r
		}(i)
	}

	readyCount.Wait()
	close(barrier)
	wg.Wait()

	return mcpJSONResult(map[string]any{
		"results": results,
		"timing":  computeTimingStats(results),
		"note":    args.Note,
	})
}

func (backend *Backend) sendParallelDifferentHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args SendParallelDifferentArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	if len(args.Requests) == 0 {
		return mcp.NewToolResultError("at least one request required"), nil
	}
	if len(args.Requests) > 50 {
		return mcp.NewToolResultError("max 50 requests"), nil
	}

	for i := range args.Requests {
		args.Requests[i] = normalizeCRLF(args.Requests[i])
	}

	count := len(args.Requests)
	results := make([]raceResult, count)
	barrier := make(chan struct{})
	var wg sync.WaitGroup
	var readyCount sync.WaitGroup

	for i := 0; i < count; i++ {
		wg.Add(1)
		readyCount.Add(1)
		go func(idx int) {
			defer wg.Done()

			conn, err := dialTarget(args.Host, args.Port, args.TLS)
			if err != nil {
				results[idx] = raceResult{Index: idx, Error: err.Error()}
				readyCount.Done()
				return
			}
			defer conn.Close()

			readyCount.Done()
			<-barrier

			start := time.Now()
			statusLine, bodyLen, err := sendAndRead(conn, []byte(args.Requests[idx]))
			elapsed := time.Since(start)

			r := raceResult{
				Index:      idx,
				StatusLine: statusLine,
				BodyLength: bodyLen,
				TimeMs:     float64(elapsed.Microseconds()) / 1000.0,
			}
			if err != nil {
				r.Error = err.Error()
			}
			results[idx] = r
		}(i)
	}

	readyCount.Wait()
	close(barrier)
	wg.Wait()

	return mcpJSONResult(map[string]any{
		"results": results,
		"timing":  computeTimingStats(results),
		"note":    args.Note,
	})
}

func (backend *Backend) sendParallelH2Handler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args SendParallelH2Args
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	if len(args.Requests) == 0 {
		return mcp.NewToolResultError("at least one request required"), nil
	}
	for i := range args.Requests {
		args.Requests[i] = normalizeCRLF(args.Requests[i])
	}
	if len(args.Requests) > 50 {
		return mcp.NewToolResultError("max 50 requests"), nil
	}

	addr := net.JoinHostPort(args.Host, fmt.Sprintf("%d", args.Port))

	tlsConn, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", addr, &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"h2"},
		ServerName:         args.Host,
	})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("TLS dial failed: %v", err)), nil
	}
	defer tlsConn.Close()

	if tlsConn.ConnectionState().NegotiatedProtocol != "h2" {
		return mcp.NewToolResultError("server did not negotiate h2"), nil
	}

	h2Transport := &http2.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
			ServerName:         args.Host,
		},
	}
	cc, err := h2Transport.NewClientConn(tlsConn)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("h2 client conn failed: %v", err)), nil
	}

	count := len(args.Requests)
	results := make([]raceResult, count)
	barrier := make(chan struct{})
	var wg sync.WaitGroup
	var readyCount sync.WaitGroup

	for i := 0; i < count; i++ {
		wg.Add(1)
		readyCount.Add(1)
		go func(idx int, rawReq string) {
			defer wg.Done()

			httpReq, parseErr := parseRawRequest(rawReq, args.Host, args.Port)
			if parseErr != nil {
				results[idx] = raceResult{Index: idx, Error: fmt.Sprintf("parse error: %v", parseErr)}
				readyCount.Done()
				return
			}

			readyCount.Done()
			<-barrier

			start := time.Now()
			resp, rtErr := cc.RoundTrip(httpReq)
			elapsed := time.Since(start)

			r := raceResult{
				Index:  idx,
				TimeMs: float64(elapsed.Microseconds()) / 1000.0,
			}

			if rtErr != nil {
				r.Error = rtErr.Error()
			} else {
				r.StatusLine = fmt.Sprintf("HTTP/2 %d %s", resp.StatusCode, resp.Status)
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				r.BodyLength = len(body)
			}
			results[idx] = r
		}(i, args.Requests[i])
	}

	readyCount.Wait()
	close(barrier)
	wg.Wait()

	return mcpJSONResult(map[string]any{
		"results": results,
		"timing":  computeTimingStats(results),
		"note":    args.Note,
		"info":    "H2 single-packet attack: all requests multiplexed on one TCP connection",
	})
}

func (backend *Backend) lastByteSyncHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args LastByteSyncArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	if args.Count < 1 {
		return mcp.NewToolResultError("count must be at least 1"), nil
	}
	if args.Count > 50 {
		args.Count = 50
	}

	args.Request = normalizeCRLF(args.Request)
	reqBytes := []byte(args.Request)
	if len(reqBytes) < 2 {
		return mcp.NewToolResultError("request must be at least 2 bytes for last-byte sync"), nil
	}

	prefix := reqBytes[:len(reqBytes)-1]
	lastByte := reqBytes[len(reqBytes)-1:]

	results := make([]raceResult, args.Count)
	barrier := make(chan struct{})
	var wg sync.WaitGroup
	var readyCount sync.WaitGroup

	for i := 0; i < args.Count; i++ {
		wg.Add(1)
		readyCount.Add(1)
		go func(idx int) {
			defer wg.Done()

			conn, err := dialTarget(args.Host, args.Port, args.TLS)
			if err != nil {
				results[idx] = raceResult{Index: idx, Error: err.Error()}
				readyCount.Done()
				return
			}
			defer conn.Close()

			conn.SetDeadline(time.Now().Add(30 * time.Second))

			if _, err := conn.Write(prefix); err != nil {
				results[idx] = raceResult{Index: idx, Error: fmt.Sprintf("prefix write error: %v", err)}
				readyCount.Done()
				return
			}

			readyCount.Done()
			<-barrier

			start := time.Now()

			if _, err := conn.Write(lastByte); err != nil {
				results[idx] = raceResult{
					Index:  idx,
					Error:  fmt.Sprintf("last byte write error: %v", err),
					TimeMs: float64(time.Since(start).Microseconds()) / 1000.0,
				}
				return
			}

			statusLine, bodyLen, readErr := sendAndReadResponse(conn)
			elapsed := time.Since(start)

			r := raceResult{
				Index:      idx,
				StatusLine: statusLine,
				BodyLength: bodyLen,
				TimeMs:     float64(elapsed.Microseconds()) / 1000.0,
			}
			if readErr != nil {
				r.Error = readErr.Error()
			}
			results[idx] = r
		}(i)
	}

	readyCount.Wait()
	close(barrier)
	wg.Wait()

	return mcpJSONResult(map[string]any{
		"results":   results,
		"timing":    computeTimingStats(results),
		"technique": "last-byte-sync",
		"note":      args.Note,
	})
}

// firstSequenceSyncHandler opens N connections in parallel, waits for ALL
// TCP/TLS handshakes to complete, then sends the full request simultaneously
// on every connection. This differs from lastByteSync (which pre-sends all but
// the last byte) and parallel (which dials and sends without strict handshake
// synchronization).
func (backend *Backend) firstSequenceSyncHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ConsolidatedRaceTestArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	host := args.Host
	port := args.Port
	useTLS := args.TLS
	rawReq := []byte(normalizeCRLF(args.Request))
	count := args.Count
	if count <= 0 || count > 50 {
		count = 10
	}

	// Phase 1: Open all connections in parallel, collect after all handshakes.
	type connResult struct {
		index int
		conn  net.Conn
		err   error
	}

	connResults := make(chan connResult, count)
	var connectWg sync.WaitGroup

	for i := 0; i < count; i++ {
		connectWg.Add(1)
		go func(idx int) {
			defer connectWg.Done()
			conn, err := dialTarget(host, port, useTLS)
			connResults <- connResult{index: idx, conn: conn, err: err}
		}(i)
	}
	connectWg.Wait()
	close(connResults)

	conns := make([]net.Conn, count)
	var connectErrors []string
	for cr := range connResults {
		if cr.err != nil {
			connectErrors = append(connectErrors, fmt.Sprintf("[%d] %v", cr.index, cr.err))
		} else {
			conns[cr.index] = cr.conn
		}
	}

	// Close all connections on exit.
	defer func() {
		for _, c := range conns {
			if c != nil {
				c.Close()
			}
		}
	}()

	if len(connectErrors) == count {
		return mcp.NewToolResultError(fmt.Sprintf("all connections failed: %v", connectErrors)), nil
	}

	// Phase 2: Barrier -- send the full request simultaneously on all
	// established connections.
	var sendBarrier sync.WaitGroup
	sendBarrier.Add(1) // single gate

	results := make([]raceResult, count)
	var wg sync.WaitGroup

	for i := 0; i < count; i++ {
		if conns[i] == nil {
			results[i] = raceResult{Index: i, Error: "connection failed"}
			continue
		}
		wg.Add(1)
		go func(idx int, conn net.Conn) {
			defer wg.Done()
			sendBarrier.Wait() // block until gate opens

			start := time.Now()
			statusLine, bodyLen, err := sendAndRead(conn, rawReq)
			elapsed := time.Since(start)

			results[idx] = raceResult{
				Index:      idx,
				StatusLine: statusLine,
				BodyLength: bodyLen,
				TimeMs:     float64(elapsed.Microseconds()) / 1000.0,
			}
			if err != nil {
				results[idx].Error = err.Error()
			}
		}(i, conns[i])
	}

	// Open the gate -- all goroutines send simultaneously.
	sendBarrier.Done()
	wg.Wait()

	stats := computeTimingStats(results)

	return mcpJSONResult(map[string]any{
		"technique":     "firstSequenceSync",
		"connections":   count,
		"connectErrors": connectErrors,
		"results":       results,
		"timing":        stats,
		"note":          args.Note,
	})
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func parseRawRequest(raw string, host string, port int) (*http.Request, error) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")

	parts := strings.SplitN(raw, "\n\n", 2)
	headerSection := parts[0]
	body := ""
	if len(parts) == 2 {
		body = parts[1]
	}

	lines := strings.Split(headerSection, "\n")
	if len(lines) == 0 {
		return nil, fmt.Errorf("empty request")
	}

	requestLine := strings.Fields(lines[0])
	if len(requestLine) < 2 {
		return nil, fmt.Errorf("invalid request line: %s", lines[0])
	}

	method := requestLine[0]
	path := requestLine[1]
	url := fmt.Sprintf("https://%s:%d%s", host, port, path)

	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		colonIdx := strings.Index(line, ":")
		if colonIdx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:colonIdx])
		value := strings.TrimSpace(line[colonIdx+1:])

		lower := strings.ToLower(key)
		if lower == "host" || lower == "connection" || lower == "transfer-encoding" {
			continue
		}
		req.Header.Set(key, value)
	}

	return req, nil
}
