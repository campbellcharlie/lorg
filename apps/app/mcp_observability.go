package app

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// mcpObsCallSeq assigns each tool call a process-unique id so a slow or failing
// call can be correlated across log lines.
var mcpObsCallSeq atomic.Uint64

// mcpObservability wraps every MCP tool handler to emit one structured log line
// per call: tool name, latency, and outcome. It is the single place lorg gains
// tool-call telemetry — mcp-go applies a ToolHandlerMiddleware to every
// registered tool, so one registration in mcpInit covers the whole surface
// without per-tool edits.
//
// outcome distinguishes the three ways a call ends:
//   - "ok"         — handler returned a normal result
//   - "tool_error" — handler ran but returned CallToolResult.IsError (the model
//     is expected to see this and self-correct)
//   - "error"      — handler returned a transport/protocol error (never reached
//     the model as a result)
func mcpObservability(next mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		callID := mcpObsCallSeq.Add(1)
		start := time.Now()

		result, err := next(ctx, request)

		outcome := "ok"
		switch {
		case err != nil:
			outcome = "error"
		case result != nil && result.IsError:
			outcome = "tool_error"
		}

		slog.Info("mcp_tool",
			"tool", request.Params.Name,
			"ms", time.Since(start).Milliseconds(),
			"outcome", outcome,
			"call", callID,
		)
		return result, err
	}
}
