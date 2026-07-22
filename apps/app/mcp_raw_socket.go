package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// Input schemas (struct-based, type-safe)
// ---------------------------------------------------------------------------

type RawSegment struct {
	Data    string `json:"data" jsonschema:"required" jsonschema_description:"Base64-encoded bytes to send"`
	DelayMs int    `json:"delayMs,omitempty" jsonschema_description:"Delay in ms before sending this segment"`
}

type SendRawTcpArgs struct {
	Host             string       `json:"host" jsonschema:"required" jsonschema_description:"Target hostname"`
	Port             int          `json:"port" jsonschema:"required" jsonschema_description:"Target port"`
	Segments         []RawSegment `json:"segments" jsonschema:"required" jsonschema_description:"Data segments to send in order"`
	ConnectTimeoutMs int          `json:"connectTimeoutMs" jsonschema:"required" jsonschema_description:"Connection timeout in ms"`
	ReadTimeoutMs    int          `json:"readTimeoutMs" jsonschema:"required" jsonschema_description:"Read timeout in ms"`
	MaxReadBytes     int          `json:"maxReadBytes" jsonschema:"required" jsonschema_description:"Maximum bytes to read from response"`
	PreviewBytes     int          `json:"previewBytes,omitempty" jsonschema_description:"Bytes to include as UTF-8 preview (default: 500)"`
}

type SendRawTlsArgs struct {
	Host               string       `json:"host" jsonschema:"required" jsonschema_description:"Target hostname"`
	Port               int          `json:"port" jsonschema:"required" jsonschema_description:"Target port"`
	Segments           []RawSegment `json:"segments" jsonschema:"required" jsonschema_description:"Data segments to send"`
	AlpnProtocols      []string     `json:"alpnProtocols,omitempty" jsonschema_description:"ALPN protocols (e.g. h2, http/1.1)"`
	InsecureSkipVerify bool         `json:"insecureSkipVerify" jsonschema:"required" jsonschema_description:"Skip TLS certificate verification"`
	ConnectTimeoutMs   int          `json:"connectTimeoutMs" jsonschema:"required" jsonschema_description:"Connection timeout in ms"`
	ReadTimeoutMs      int          `json:"readTimeoutMs" jsonschema:"required" jsonschema_description:"Read timeout in ms"`
	MaxReadBytes       int          `json:"maxReadBytes" jsonschema:"required" jsonschema_description:"Max bytes to read"`
	PreviewBytes       int          `json:"previewBytes,omitempty" jsonschema_description:"Bytes for UTF-8 preview (default: 500)"`
}

type H2SequenceRequest struct {
	RawRequest string `json:"rawRequest" jsonschema:"required" jsonschema_description:"Raw HTTP request to send"`
	DelayMs    int    `json:"delayMs,omitempty" jsonschema_description:"Delay before sending this request (ms)"`
}

type SendHttp2SequenceArgs struct {
	Host               string              `json:"host" jsonschema:"required" jsonschema_description:"Target hostname"`
	Port               int                 `json:"port" jsonschema:"required" jsonschema_description:"Target port"`
	Requests           []H2SequenceRequest `json:"requests" jsonschema:"required" jsonschema_description:"HTTP requests to send sequentially on same connection"`
	InsecureSkipVerify bool                `json:"insecureSkipVerify" jsonschema:"required" jsonschema_description:"Skip TLS verification"`
	ConnectTimeoutMs   int                 `json:"connectTimeoutMs" jsonschema:"required" jsonschema_description:"Connection timeout in ms"`
	ReadTimeoutMs      int                 `json:"readTimeoutMs" jsonschema:"required" jsonschema_description:"Read timeout per request in ms"`
	MaxReadBytes       int                 `json:"maxReadBytes" jsonschema:"required" jsonschema_description:"Max bytes to read per response"`
	PreviewBytes       int                 `json:"previewBytes,omitempty" jsonschema_description:"Preview bytes (default: 500)"`
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// readResult captures a raw read outcome. For a timeout-differential desync
// oracle the load-bearing distinction is (timedOut && !complete) — a socket that
// hit its deadline WITHOUT a complete HTTP response, i.e. the server is still
// waiting for request bytes. A complete response that arrives on a kept-alive
// connection sets complete=true and timedOut=false even though the socket then
// sits idle, so a normal keep-alive reply is never misread as a hang.
type readResult struct {
	data     []byte
	timedOut bool
	complete bool // a complete HTTP/1 response was parsed from data
	elapsed  time.Duration
}

// readResponseTimed reads up to maxBytes within the given timeout. It stops as
// soon as a complete HTTP/1 response is present (so an idle keep-alive socket is
// not misclassified as a hang), on EOF/error, on maxBytes, or on the deadline —
// the last of which sets timedOut.
func readResponseTimed(conn net.Conn, readTimeout time.Duration, maxBytes int) readResult {
	start := time.Now()
	conn.SetReadDeadline(time.Now().Add(readTimeout))
	buf := make([]byte, 4096)
	var response []byte
	var timedOut bool

	for {
		n, err := conn.Read(buf)
		if n > 0 {
			response = append(response, buf[:n]...)
		}
		if responseComplete(response) {
			break // full response in hand — stop before the deadline
		}
		if len(response) >= maxBytes {
			response = response[:maxBytes]
			break
		}
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				timedOut = true
			}
			break // EOF, timeout, or error
		}
	}

	return readResult{
		data:     response,
		timedOut: timedOut,
		complete: responseComplete(response),
		elapsed:  time.Since(start),
	}
}

// responseComplete reports whether data holds a complete HTTP/1 response. It
// handles Content-Length, chunked framing, and bodyless statuses (1xx/204/304).
// A response with neither Content-Length nor chunked framing is delimited by
// connection close, so this returns false for it (the read loop still ends on
// EOF).
func responseComplete(data []byte) bool {
	i := bytes.Index(data, []byte("\r\n\r\n"))
	if i < 0 {
		return false // headers not fully received yet
	}
	head := data[:i]
	body := data[i+4:]

	status := responseStatusCode(head)
	if status >= 100 && status < 200 { // interim 1xx — the real response follows
		return false
	}
	if status == 204 || status == 304 { // defined to have no body
		return true
	}

	lower := bytes.ToLower(head)
	if bytes.Contains(lower, []byte("\r\ntransfer-encoding:")) && bytes.Contains(lower, []byte("chunked")) {
		return bytes.Contains(body, []byte("0\r\n\r\n"))
	}
	if cl, ok := responseContentLength(lower); ok {
		return len(body) >= cl
	}
	return false // no CL, no chunked → terminated by connection close
}

func responseStatusCode(head []byte) int {
	line := head
	if j := bytes.IndexByte(line, '\n'); j >= 0 {
		line = line[:j]
	}
	fields := bytes.Fields(line) // HTTP/1.x <code> <reason>
	if len(fields) < 2 {
		return 0
	}
	code, _ := strconv.Atoi(string(fields[1]))
	return code
}

func responseContentLength(lowerHead []byte) (int, bool) {
	key := []byte("\r\ncontent-length:")
	k := bytes.Index(lowerHead, key)
	if k < 0 {
		return 0, false
	}
	rest := lowerHead[k+len(key):]
	if e := bytes.IndexByte(rest, '\r'); e >= 0 {
		rest = rest[:e]
	}
	n, err := strconv.Atoi(string(bytes.TrimSpace(rest)))
	if err != nil {
		return 0, false
	}
	return n, true
}

// readResponse preserves the original signature for callers that don't need the
// timing/timeout signal. It never returns a non-nil error (partial reads are
// expected); use readResponseTimed when the deadline outcome matters.
func readResponse(conn net.Conn, readTimeout time.Duration, maxBytes int) ([]byte, error) {
	return readResponseTimed(conn, readTimeout, maxBytes).data, nil
}

// toPreview converts raw bytes to a printable UTF-8 string, replacing
// non-printable characters with '.' for safe display.
func toPreview(data []byte, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = 500
	}
	if len(data) > maxBytes {
		data = data[:maxBytes]
	}
	preview := make([]byte, len(data))
	for i, b := range data {
		if b >= 32 && b < 127 || b == '\n' || b == '\r' || b == '\t' {
			preview[i] = b
		} else {
			preview[i] = '.'
		}
	}
	return string(preview)
}

// sendSegments writes each RawSegment to the connection in order, applying
// optional per-segment delays. Returns the total number of bytes written.
func sendSegments(conn net.Conn, segments []RawSegment) (int, error) {
	totalSent := 0
	for i, seg := range segments {
		decoded, err := base64.StdEncoding.DecodeString(seg.Data)
		if err != nil {
			return totalSent, fmt.Errorf("segment %d: base64 decode failed: %w", i, err)
		}
		if seg.DelayMs > 0 {
			time.Sleep(time.Duration(seg.DelayMs) * time.Millisecond)
		}
		n, err := conn.Write(decoded)
		totalSent += n
		if err != nil {
			return totalSent, fmt.Errorf("segment %d: write failed: %w", i, err)
		}
	}
	return totalSent, nil
}

// ---------------------------------------------------------------------------
// Tool handlers
// ---------------------------------------------------------------------------

func (backend *Backend) sendRawTcpHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args SendRawTcpArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	addr := net.JoinHostPort(args.Host, fmt.Sprintf("%d", args.Port))
	connectTimeout := time.Duration(args.ConnectTimeoutMs) * time.Millisecond
	readTimeout := time.Duration(args.ReadTimeoutMs) * time.Millisecond

	conn, err := net.DialTimeout("tcp", addr, connectTimeout)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("TCP connect to %s failed: %v", addr, err)), nil
	}
	defer conn.Close()

	totalSent, err := sendSegments(conn, args.Segments)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	read := readResponseTimed(conn, readTimeout, args.MaxReadBytes)
	response := read.data

	return mcpJSONResult(map[string]any{
		"responseBase64":  base64.StdEncoding.EncodeToString(response),
		"responsePreview": toPreview(response, args.PreviewBytes),
		"bytesRead":       len(response),
		"bytesSent":       totalSent,
		"timedOut":        read.timedOut,
		"readMs":          read.elapsed.Milliseconds(),
	})
}

func (backend *Backend) sendRawTlsHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args SendRawTlsArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	addr := net.JoinHostPort(args.Host, fmt.Sprintf("%d", args.Port))
	connectTimeout := time.Duration(args.ConnectTimeoutMs) * time.Millisecond
	readTimeout := time.Duration(args.ReadTimeoutMs) * time.Millisecond

	tlsConfig := &tls.Config{
		InsecureSkipVerify: args.InsecureSkipVerify,
		ServerName:         args.Host,
	}
	if len(args.AlpnProtocols) > 0 {
		tlsConfig.NextProtos = args.AlpnProtocols
	}

	dialer := &net.Dialer{Timeout: connectTimeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, tlsConfig)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("TLS connect to %s failed: %v", addr, err)), nil
	}
	defer conn.Close()

	totalSent, err := sendSegments(conn, args.Segments)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	read := readResponseTimed(conn, readTimeout, args.MaxReadBytes)
	response := read.data

	negotiatedProtocol := conn.ConnectionState().NegotiatedProtocol

	return mcpJSONResult(map[string]any{
		"responseBase64":     base64.StdEncoding.EncodeToString(response),
		"responsePreview":    toPreview(response, args.PreviewBytes),
		"bytesRead":          len(response),
		"bytesSent":          totalSent,
		"negotiatedProtocol": negotiatedProtocol,
		"timedOut":           read.timedOut,
		"readMs":             read.elapsed.Milliseconds(),
	})
}

func (backend *Backend) sendHttp2SequenceHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args SendHttp2SequenceArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	addr := net.JoinHostPort(args.Host, fmt.Sprintf("%d", args.Port))
	connectTimeout := time.Duration(args.ConnectTimeoutMs) * time.Millisecond
	readTimeout := time.Duration(args.ReadTimeoutMs) * time.Millisecond

	tlsConfig := &tls.Config{
		InsecureSkipVerify: args.InsecureSkipVerify,
		ServerName:         args.Host,
		NextProtos:         []string{"h2", "http/1.1"},
	}

	dialer := &net.Dialer{Timeout: connectTimeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, tlsConfig)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("TLS connect to %s failed: %v", addr, err)), nil
	}
	defer conn.Close()

	negotiatedProtocol := conn.ConnectionState().NegotiatedProtocol

	type sequenceResult struct {
		Index           int    `json:"index"`
		ResponsePreview string `json:"responsePreview"`
		BytesRead       int    `json:"bytesRead"`
		TimeMs          int64  `json:"timeMs"`
		Error           string `json:"error,omitempty"`
	}

	results := make([]sequenceResult, 0, len(args.Requests))

	for i, req := range args.Requests {
		if req.DelayMs > 0 {
			time.Sleep(time.Duration(req.DelayMs) * time.Millisecond)
		}

		start := time.Now()

		// Normalize line endings to CRLF for HTTP compliance
		rawBytes := []byte(strings.ReplaceAll(req.RawRequest, "\n", "\r\n"))

		_, writeErr := conn.Write(rawBytes)
		if writeErr != nil {
			results = append(results, sequenceResult{
				Index:  i,
				TimeMs: time.Since(start).Milliseconds(),
				Error:  fmt.Sprintf("write failed: %v", writeErr),
			})
			// Connection is likely broken; stop sending further requests.
			break
		}

		response, _ := readResponse(conn, readTimeout, args.MaxReadBytes)
		elapsed := time.Since(start).Milliseconds()

		results = append(results, sequenceResult{
			Index:           i,
			ResponsePreview: toPreview(response, args.PreviewBytes),
			BytesRead:       len(response),
			TimeMs:          elapsed,
		})
	}

	return mcpJSONResult(map[string]any{
		"results":            results,
		"negotiatedProtocol": negotiatedProtocol,
		"connectionReused":   true,
	})
}
