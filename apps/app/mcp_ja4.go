package app

import (
	"context"

	"github.com/campbellcharlie/lorg/lrx/rawproxy"
	"github.com/mark3labs/mcp-go/mcp"
)

type JA4Args struct {
	Action string `json:"action" jsonschema:"required,enum=lookup,list" jsonschema_description:"lookup: get JA4 for a host; list: all cached fingerprints"`
	Host   string `json:"host,omitempty" jsonschema_description:"Hostname to look up"`
}

func (backend *Backend) ja4Handler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args JA4Args
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "lookup":
		if args.Host == "" {
			return mcp.NewToolResultError("host is required for lookup"), nil
		}
		fp, found := rawproxy.GetJA4(args.Host)
		if !found {
			return mcp.NewToolResultError("no JA4 fingerprint cached for " + args.Host + ". Traffic must pass through the proxy first."), nil
		}
		return mcpJSONResult(fp)

	case "list":
		fps := rawproxy.GetAllJA4()
		return mcpJSONResult(map[string]any{
			"fingerprints": fps,
			"count":        len(fps),
		})

	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: lookup, list"), nil
	}
}
