package app

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// TestMCPDispatch exercises the MCP dispatch/marshalling layer end-to-end via an
// in-process client: JSON args -> BindArguments -> action switch -> schema ->
// result encoding -> JSON out. The existing *_test.go files call the typed leaf
// handlers directly (e.g. oobStartHandler(OOBArgs{...})), which structurally
// skip this layer. This is the one seam none of them cover.
//
// It deliberately uses DB-free tools (lorgStatus, encode) so the test asserts on
// dispatch behaviour, not handler business logic — no SQLite fixture required.
func newDispatchTestClient(t *testing.T) *client.Client {
	t.Helper()

	backend := &Backend{}
	backend.mcpInit()
	if backend.MCP == nil || backend.MCP.server == nil {
		t.Fatal("mcpInit did not build an MCP server")
	}

	c, err := client.NewInProcessClient(backend.MCP.server)
	if err != nil {
		t.Fatalf("NewInProcessClient: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("client.Start: %v", err)
	}
	if _, err := c.Initialize(context.Background(), mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcp.Implementation{Name: "dispatch-test", Version: "1.0.0"},
		},
	}); err != nil {
		t.Fatalf("client.Initialize: %v", err)
	}
	return c
}

func callText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestMCPDispatch(t *testing.T) {
	c := newDispatchTestClient(t)
	ctx := context.Background()

	// tools/list round-trips and the surface is non-empty.
	t.Run("list_tools", func(t *testing.T) {
		res, err := c.ListTools(ctx, mcp.ListToolsRequest{})
		if err != nil {
			t.Fatalf("ListTools: %v", err)
		}
		if len(res.Tools) == 0 {
			t.Fatal("expected a non-empty tool list")
		}
		var haveStatus bool
		for _, tool := range res.Tools {
			if tool.Name == "lorgStatus" {
				haveStatus = true
			}
		}
		if !haveStatus {
			t.Error("lorgStatus missing from tools/list")
		}
	})

	// Full call round-trip on a DB-free tool: proves arg-decode -> handler ->
	// result-encode works through the real MCP path.
	t.Run("call_ok", func(t *testing.T) {
		res, err := c.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{Name: "lorgStatus"},
		})
		if err != nil {
			t.Fatalf("CallTool lorgStatus: %v", err)
		}
		if res.IsError {
			t.Fatalf("lorgStatus flagged as error: %s", callText(t, res))
		}
		if callText(t, res) == "" {
			t.Error("expected non-empty result body")
		}
	})

	// BindArguments + consolidated action switch + real work + result encoding.
	t.Run("call_action_ok", func(t *testing.T) {
		res, err := c.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Name:      "encode",
				Arguments: map[string]any{"action": "b64Encode", "content": "lorg"},
			},
		})
		if err != nil {
			t.Fatalf("CallTool encode: %v", err)
		}
		if res.IsError {
			t.Fatalf("encode b64Encode flagged as error: %s", callText(t, res))
		}
		if got := callText(t, res); !strings.Contains(got, "bG9yZw") {
			t.Errorf("expected base64 of \"lorg\" (bG9yZw==) in result, got: %s", got)
		}
	})

	// The action-switch default must surface as an IsError tool result (not a
	// transport error, not a panic) — this is the model-visible error contract.
	t.Run("call_unknown_action_is_error", func(t *testing.T) {
		res, err := c.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Name:      "encode",
				Arguments: map[string]any{"action": "definitely-not-real"},
			},
		})
		if err != nil {
			t.Fatalf("CallTool encode(bad action) returned transport error: %v", err)
		}
		if !res.IsError {
			t.Error("expected IsError for unknown action")
		}
	})

	// An unknown tool name must be rejected by dispatch (transport-level error).
	t.Run("call_unknown_tool_rejected", func(t *testing.T) {
		_, err := c.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{Name: "nope_not_a_real_tool"},
		})
		if err == nil {
			t.Error("expected an error calling an unregistered tool")
		}
	})
}
