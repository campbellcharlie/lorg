package app

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
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

// readResponse reads up to maxBytes from a connection within the given timeout.
// It returns whatever data was read, swallowing EOF and timeout errors since
// partial reads are expected for raw socket operations.
func readResponse(conn net.Conn, readTimeout time.Duration, maxBytes int) ([]byte, error) {
	conn.SetReadDeadline(time.Now().Add(readTimeout))
	buf := make([]byte, 4096)
	var response []byte

	for {
		n, err := conn.Read(buf)
		if n > 0 {
			response = append(response, buf[:n]...)
		}
		if len(response) >= maxBytes {
			response = response[:maxBytes]
			break
		}
		if err != nil {
			break // EOF, timeout, or error
		}
	}

	return response, nil
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

	response, err := readResponse(conn, readTimeout, args.MaxReadBytes)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("read failed: %v", err)), nil
	}

	return mcpJSONResult(map[string]any{
		"responseBase64":  base64.StdEncoding.EncodeToString(response),
		"responsePreview": toPreview(response, args.PreviewBytes),
		"bytesRead":       len(response),
		"bytesSent":       totalSent,
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

	response, err := readResponse(conn, readTimeout, args.MaxReadBytes)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("read failed: %v", err)), nil
	}

	negotiatedProtocol := conn.ConnectionState().NegotiatedProtocol

	return mcpJSONResult(map[string]any{
		"responseBase64":     base64.StdEncoding.EncodeToString(response),
		"responsePreview":    toPreview(response, args.PreviewBytes),
		"bytesRead":          len(response),
		"bytesSent":          totalSent,
		"negotiatedProtocol": negotiatedProtocol,
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
