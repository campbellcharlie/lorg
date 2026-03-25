package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/url"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// Input schemas (struct-based, type-safe)
// ---------------------------------------------------------------------------

type URLEncodeArgs struct {
	Content string `json:"content" jsonschema:"required" jsonschema_description:"The string to URL-encode"`
}

type URLDecodeArgs struct {
	Content string `json:"content" jsonschema:"required" jsonschema_description:"The URL-encoded string to decode"`
}

type Base64EncodeArgs struct {
	Content string `json:"content" jsonschema:"required" jsonschema_description:"The string to base64-encode"`
}

type Base64DecodeArgs struct {
	Content string `json:"content" jsonschema:"required" jsonschema_description:"The base64-encoded string to decode"`
}

type GenerateRandomStringArgs struct {
	Length  int    `json:"length" jsonschema:"required" jsonschema_description:"The length of the random string to generate"`
	Charset string `json:"charset,omitempty" jsonschema_description:"The character set to use (default: alphanumeric a-zA-Z0-9)"`
}

// ---------------------------------------------------------------------------
// Encoding tool handlers
// ---------------------------------------------------------------------------

func (backend *Backend) urlEncodeHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args URLEncodeArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	encoded := url.QueryEscape(args.Content)

	return mcpJSONResult(map[string]any{
		"encoded": encoded,
	})
}

func (backend *Backend) urlDecodeHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args URLDecodeArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	decoded, err := url.QueryUnescape(args.Content)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to URL-decode: %v", err)), nil
	}

	return mcpJSONResult(map[string]any{
		"decoded": decoded,
	})
}

func (backend *Backend) base64EncodeHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args Base64EncodeArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	encoded := base64.StdEncoding.EncodeToString([]byte(args.Content))

	return mcpJSONResult(map[string]any{
		"encoded": encoded,
	})
}

func (backend *Backend) base64DecodeHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args Base64DecodeArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	decoded, err := base64.StdEncoding.DecodeString(args.Content)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to base64-decode: %v", err)), nil
	}

	return mcpJSONResult(map[string]any{
		"decoded": string(decoded),
	})
}

func (backend *Backend) generateRandomStringHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args GenerateRandomStringArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	charset := args.Charset
	if charset == "" {
		charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	}

	if args.Length <= 0 {
		return mcp.NewToolResultError("length must be greater than 0"), nil
	}

	charsetLen := big.NewInt(int64(len(charset)))
	result := make([]byte, args.Length)
	for i := 0; i < args.Length; i++ {
		idx, err := rand.Int(rand.Reader, charsetLen)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to generate random string: %v", err)), nil
		}
		result[i] = charset[idx.Int64()]
	}

	return mcpJSONResult(map[string]any{
		"value":   string(result),
		"length":  args.Length,
		"charset": charset,
	})
}
