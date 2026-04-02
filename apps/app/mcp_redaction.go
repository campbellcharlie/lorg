package app

import (
	"context"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// Redaction configuration
// ---------------------------------------------------------------------------

type redactionMode string

const (
	redactionOff      redactionMode = "off"
	redactionBalanced redactionMode = "balanced"
	redactionStrict   redactionMode = "strict"
)

var (
	currentRedactionMode redactionMode = redactionOff
	redactionMu          sync.RWMutex
)

// sensitiveHeaders lists headers whose values should be redacted.
var sensitiveHeaders = []string{
	"authorization",
	"cookie",
	"set-cookie",
	"x-api-key",
	"x-auth-token",
	"proxy-authorization",
}

// ---------------------------------------------------------------------------
// Redaction logic
// ---------------------------------------------------------------------------

func getRedactionMode() redactionMode {
	redactionMu.RLock()
	defer redactionMu.RUnlock()
	return currentRedactionMode
}

func setRedactionMode(mode redactionMode) {
	redactionMu.Lock()
	defer redactionMu.Unlock()
	currentRedactionMode = mode
}

// redactHeaders redacts sensitive header values in a raw HTTP message.
func redactHeaders(raw string) string {
	mode := getRedactionMode()
	if mode == redactionOff {
		return raw
	}

	lines := strings.Split(raw, "\r\n")
	for i, line := range lines {
		if line == "" {
			break // reached body separator
		}
		colonIdx := strings.IndexByte(line, ':')
		if colonIdx < 0 {
			continue
		}
		headerName := strings.ToLower(strings.TrimSpace(line[:colonIdx]))
		for _, sensitive := range sensitiveHeaders {
			if headerName == sensitive {
				if mode == redactionStrict {
					lines[i] = line[:colonIdx+1] + " [REDACTED]"
				} else if mode == redactionBalanced {
					// Keep header name + key names for cookies, redact values
					if headerName == "cookie" || headerName == "set-cookie" {
						lines[i] = redactCookieValues(line, colonIdx)
					} else {
						lines[i] = line[:colonIdx+1] + " [REDACTED]"
					}
				}
				break
			}
		}

		// Also catch bearer tokens in any header
		if mode == redactionStrict {
			if colonIdx > 0 {
				value := strings.TrimSpace(line[colonIdx+1:])
				if strings.HasPrefix(strings.ToLower(value), "bearer ") {
					lines[i] = line[:colonIdx+1] + " Bearer [REDACTED]"
				}
			}
		}
	}

	return strings.Join(lines, "\r\n")
}

// redactCookieValues keeps cookie names but redacts values.
func redactCookieValues(line string, colonIdx int) string {
	prefix := line[:colonIdx+2] // "Cookie: " or "Set-Cookie: "
	value := strings.TrimSpace(line[colonIdx+1:])

	parts := strings.Split(value, ";")
	var redacted []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		eqIdx := strings.IndexByte(part, '=')
		if eqIdx >= 0 {
			name := part[:eqIdx]
			redacted = append(redacted, name+"=[REDACTED]")
		} else {
			redacted = append(redacted, part) // flags like HttpOnly, Secure
		}
	}
	return prefix + strings.Join(redacted, "; ")
}

// redactResponse wraps a traffic response map to redact sensitive data.
func redactResponse(data map[string]any) map[string]any {
	mode := getRedactionMode()
	if mode == redactionOff {
		return data
	}

	// Deep copy to avoid mutating original
	result := make(map[string]any)
	for k, v := range data {
		result[k] = v
	}

	// Redact raw request/response strings if present
	if raw, ok := result["raw_request"].(string); ok {
		result["raw_request"] = redactHeaders(raw)
	}
	if raw, ok := result["raw_response"].(string); ok {
		result["raw_response"] = redactHeaders(raw)
	}
	if raw, ok := result["request"].(string); ok {
		result["request"] = redactHeaders(raw)
	}
	if raw, ok := result["response"].(string); ok {
		result["response"] = redactHeaders(raw)
	}

	return result
}

// ---------------------------------------------------------------------------
// MCP handlers (integrated into project tool via action dispatch)
// ---------------------------------------------------------------------------

func (backend *Backend) setRedactionModeHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	type args struct {
		Mode string `json:"redactionMode" jsonschema:"required,enum=off,balanced,strict"`
	}
	var a args
	if err := request.BindArguments(&a); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch redactionMode(a.Mode) {
	case redactionOff, redactionBalanced, redactionStrict:
		setRedactionMode(redactionMode(a.Mode))
	default:
		return mcp.NewToolResultError("invalid mode. Valid: off, balanced, strict"), nil
	}

	return mcpJSONResult(map[string]any{
		"success": true,
		"mode":    string(getRedactionMode()),
	})
}

func (backend *Backend) getRedactionModeHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return mcpJSONResult(map[string]any{
		"mode": string(getRedactionMode()),
	})
}
