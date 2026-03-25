package app

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// Input schemas
// ---------------------------------------------------------------------------

type GetCookieJarArgs struct {
	Domain  string `json:"domain,omitempty" jsonschema_description:"Filter cookies by domain (not implemented yet, returns all session cookies)"`
	Session string `json:"session,omitempty" jsonschema_description:"Session name (default: active session)"`
}

type SetCookieArgs struct {
	Name    string `json:"name" jsonschema:"required" jsonschema_description:"Cookie name"`
	Value   string `json:"value" jsonschema:"required" jsonschema_description:"Cookie value"`
	Session string `json:"session,omitempty" jsonschema_description:"Session name (default: active session)"`
}

// ---------------------------------------------------------------------------
// Tool handlers
// ---------------------------------------------------------------------------

func (backend *Backend) getCookieJarHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args GetCookieJarArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	session, err := backend.resolveSession(args.Session)
	if err != nil {
		return mcp.NewToolResultError("no session found: create one with sessionCreate and activate with sessionSwitch"), nil
	}

	cookies := session.Get("cookies")
	cookieMap, ok := cookies.(map[string]any)
	if !ok {
		cookieMap = make(map[string]any)
	}

	return mcpJSONResult(map[string]any{
		"session": session.GetString("name"),
		"cookies": cookieMap,
		"count":   len(cookieMap),
	})
}

func (backend *Backend) setCookieHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args SetCookieArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	session, err := backend.resolveSession(args.Session)
	if err != nil {
		return mcp.NewToolResultError("no session found: create one with sessionCreate and activate with sessionSwitch"), nil
	}

	dao := backend.App.Dao()

	cookies := session.Get("cookies")
	cookieMap, ok := cookies.(map[string]any)
	if !ok {
		cookieMap = make(map[string]any)
	}

	cookieMap[args.Name] = args.Value
	session.Set("cookies", cookieMap)

	if err := dao.SaveRecord(session); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to save cookie: %v", err)), nil
	}

	return mcpJSONResult(map[string]any{
		"success":      true,
		"session":      session.GetString("name"),
		"cookie":       map[string]string{"name": args.Name, "value": args.Value},
		"totalCookies": len(cookieMap),
	})
}
