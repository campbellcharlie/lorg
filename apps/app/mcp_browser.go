package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// CamoFox REST API client
//
// CamoFox is a headless Firefox-based browser that routes traffic through
// lorg's proxy. All browser interactions for pentesting go through this
// API rather than Chrome DevTools to ensure traffic visibility.
// ---------------------------------------------------------------------------

// camofoxURL is the CamoFox browser server endpoint.
// Package-level because helper functions (camofoxGet, camofoxPost, etc.) are called
// from multiple handlers without Backend reference. Configurable via browserConfig tool.
var camofoxURL = "http://localhost:9377"

// camofoxUserID identifies the CamoFox user/profile.
// Package-level for the same reasons as camofoxURL.
var camofoxUserID = "default"

// camofoxRequest makes an HTTP request to the CamoFox server and returns
// the parsed JSON response. Non-JSON responses are wrapped in a map with
// "raw" and "status" keys.
func camofoxRequest(method, path string, body any) (map[string]any, error) {
	var reqBody io.Reader
	if body != nil {
		jsonBytes, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal error: %w", err)
		}
		reqBody = bytes.NewReader(jsonBytes)
	}

	req, err := http.NewRequest(method, camofoxURL+path, reqBody)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("camofox request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response failed: %w", err)
	}

	var result map[string]any
	if err := json.Unmarshal(respBody, &result); err != nil {
		// Return raw text if not JSON
		return map[string]any{"raw": string(respBody), "status": resp.StatusCode}, nil
	}

	if resp.StatusCode >= 400 {
		errMsg := ""
		if msg, ok := result["message"].(string); ok {
			errMsg = msg
		} else if msg, ok := result["error"].(string); ok {
			errMsg = msg
		} else {
			errMsg = string(respBody)
		}
		return nil, fmt.Errorf("camofox error (%d): %s", resp.StatusCode, errMsg)
	}

	return result, nil
}

// camofoxRequestRaw is like camofoxRequest but returns the raw response body
// bytes and status code, useful when the response may be an array or non-object.
func camofoxRequestRaw(method, path string, body any) ([]byte, int, error) {
	var reqBody io.Reader
	if body != nil {
		jsonBytes, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal error: %w", err)
		}
		reqBody = bytes.NewReader(jsonBytes)
	}

	req, err := http.NewRequest(method, camofoxURL+path, reqBody)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("camofox request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response failed: %w", err)
	}

	if resp.StatusCode >= 400 {
		return respBody, resp.StatusCode, fmt.Errorf("camofox error (%d): %s", resp.StatusCode, string(respBody))
	}

	return respBody, resp.StatusCode, nil
}

// camofoxGet is a convenience for GET requests that appends userId as a query parameter.
func camofoxGet(path string) (map[string]any, error) {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return camofoxRequest("GET", path+sep+"userId="+camofoxUserID, nil)
}

// camofoxGetRaw is a convenience for GET requests returning raw bytes.
func camofoxGetRaw(path string) ([]byte, int, error) {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return camofoxRequestRaw("GET", path+sep+"userId="+camofoxUserID, nil)
}

// camofoxPost is a convenience for POST requests that auto-injects userId and sessionKey into the body.
func camofoxPost(path string, extraFields map[string]any) (map[string]any, error) {
	body := map[string]any{
		"userId":     camofoxUserID,
		"sessionKey": camofoxUserID, // CamoFox requires sessionKey for tab creation
	}
	for k, v := range extraFields {
		body[k] = v
	}
	return camofoxRequest("POST", path, body)
}

// camofoxDelete is a convenience for DELETE requests.
func camofoxDelete(path string) (map[string]any, error) {
	return camofoxRequest("DELETE", path, nil)
}

// ---------------------------------------------------------------------------
// Tool 1: browser -- tab lifecycle + observation
// ---------------------------------------------------------------------------

// BrowserArgs is the union argument struct for the browser tool.
type BrowserArgs struct {
	Action   string `json:"action" jsonschema:"required" jsonschema_description:"Operation: open, close, list, navigate, back, forward, refresh, screenshot, snapshot"`
	TabID    string `json:"tabId,omitempty" jsonschema_description:"Tab ID (required for most actions except open and list)"`
	URL      string `json:"url,omitempty" jsonschema_description:"URL to navigate to (open, navigate)"`
	Preset   string `json:"preset,omitempty" jsonschema_description:"Geo preset: us-east, us-west, japan, uk, germany, vietnam, singapore, australia (open)"`
	FullPage bool   `json:"fullPage,omitempty" jsonschema_description:"Capture full page screenshot (screenshot)"`
}

func (backend *Backend) browserHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args BrowserArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "open":
		fields := map[string]any{}
		if args.URL != "" {
			fields["url"] = args.URL
		}
		if args.Preset != "" {
			fields["preset"] = args.Preset
		}
		result, err := camofoxPost("/tabs", fields)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to open tab: %v", err)), nil
		}
		return mcpJSONResult(result)

	case "close":
		if args.TabID == "" {
			return mcp.NewToolResultError("tabId is required for close"), nil
		}
		result, err := camofoxDelete("/tabs/" + args.TabID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to close tab: %v", err)), nil
		}
		return mcpJSONResult(result)

	case "list":
		result, err := camofoxGet("/tabs")
		if err != nil {
			// The /tabs endpoint may return an array, try raw
			raw, _, rawErr := camofoxGetRaw("/tabs")
			if rawErr != nil {
				return mcp.NewToolResultError(fmt.Sprintf("failed to list tabs: %v", err)), nil
			}
			var tabs []any
			if jsonErr := json.Unmarshal(raw, &tabs); jsonErr != nil {
				return mcp.NewToolResultError(fmt.Sprintf("failed to parse tabs: %v", jsonErr)), nil
			}
			return mcpJSONResult(map[string]any{"tabs": tabs, "count": len(tabs)})
		}
		return mcpJSONResult(result)

	case "navigate":
		if args.TabID == "" {
			return mcp.NewToolResultError("tabId is required for navigate"), nil
		}
		if args.URL == "" {
			return mcp.NewToolResultError("url is required for navigate"), nil
		}
		result, err := camofoxPost("/tabs/"+args.TabID+"/navigate", map[string]any{"url": args.URL})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to navigate: %v", err)), nil
		}
		return mcpJSONResult(result)

	case "back":
		if args.TabID == "" {
			return mcp.NewToolResultError("tabId is required for back"), nil
		}
		result, err := camofoxPost("/tabs/"+args.TabID+"/back", nil)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to go back: %v", err)), nil
		}
		return mcpJSONResult(result)

	case "forward":
		if args.TabID == "" {
			return mcp.NewToolResultError("tabId is required for forward"), nil
		}
		result, err := camofoxPost("/tabs/"+args.TabID+"/forward", nil)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to go forward: %v", err)), nil
		}
		return mcpJSONResult(result)

	case "refresh":
		if args.TabID == "" {
			return mcp.NewToolResultError("tabId is required for refresh"), nil
		}
		result, err := camofoxPost("/tabs/"+args.TabID+"/refresh", nil)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to refresh: %v", err)), nil
		}
		return mcpJSONResult(result)

	case "screenshot":
		if args.TabID == "" {
			return mcp.NewToolResultError("tabId is required for screenshot"), nil
		}
		// CamoFox returns raw PNG bytes, not JSON
		sep := "?"
		screenshotURL := camofoxURL + "/tabs/" + args.TabID + "/screenshot" + sep + "userId=" + camofoxUserID
		req, err := http.NewRequest("GET", screenshotURL, nil)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to create screenshot request: %v", err)), nil
		}
		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to take screenshot: %v", err)), nil
		}
		defer resp.Body.Close()
		pngBytes, err := io.ReadAll(resp.Body)
		if err != nil || len(pngBytes) == 0 {
			return mcp.NewToolResultError("failed to read screenshot data"), nil
		}
		if resp.StatusCode >= 400 {
			preview := string(pngBytes)
			if len(preview) > 200 {
				preview = preview[:200]
			}
			return mcp.NewToolResultError(fmt.Sprintf("screenshot failed (%d): %s", resp.StatusCode, preview)), nil
		}
		// Save to project screenshots directory
		screenshotDir := "Projects/screenshots"
		if pInfo := projectDB.Info(); pInfo["projectName"] != nil && pInfo["projectName"] != "" {
			screenshotDir = fmt.Sprintf("Projects/%s/screenshots", pInfo["projectName"])
		}
		os.MkdirAll(screenshotDir, 0755)
		timestamp := time.Now().Format("20060102-150405")
		filename := fmt.Sprintf("screenshot-%s.png", timestamp)
		savePath := filepath.Join(screenshotDir, filename)
		if writeErr := os.WriteFile(savePath, pngBytes, 0644); writeErr != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to save screenshot: %v", writeErr)), nil
		}
		absPath, _ := filepath.Abs(savePath)
		return mcpJSONResult(map[string]any{
			"filePath": absPath,
			"size":     len(pngBytes),
			"format":   "png",
		})

	case "snapshot":
		if args.TabID == "" {
			return mcp.NewToolResultError("tabId is required for snapshot"), nil
		}
		result, err := camofoxGet("/tabs/" + args.TabID + "/snapshot")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to take snapshot: %v", err)), nil
		}
		// Extract the accessibility tree text and ref count
		snapshot := ""
		if s, ok := result["snapshot"].(string); ok {
			snapshot = s
		}
		pageURL := ""
		if u, ok := result["url"].(string); ok {
			pageURL = u
		}
		refsCount := 0
		if rc, ok := result["refsCount"].(float64); ok {
			refsCount = int(rc)
		}
		return mcpJSONResult(map[string]any{
			"url":       pageURL,
			"snapshot":  snapshot,
			"refsCount": refsCount,
		})

	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: open, close, list, navigate, back, forward, refresh, screenshot, snapshot"), nil
	}
}

// ---------------------------------------------------------------------------
// Tool 2: browserInteract -- page interaction
// ---------------------------------------------------------------------------

// BrowserInteractArgs is the union argument struct for the browserInteract tool.
type BrowserInteractArgs struct {
	Action    string `json:"action" jsonschema:"required" jsonschema_description:"Operation: click, type, fill, press, scroll, hover, waitForText, waitForSelector"`
	TabID     string `json:"tabId" jsonschema:"required" jsonschema_description:"Tab ID"`
	Ref       string `json:"ref,omitempty" jsonschema_description:"Element ref from snapshot (e.g. e1, e2)"`
	Selector  string `json:"selector,omitempty" jsonschema_description:"CSS selector (fallback if no ref)"`
	Text      string `json:"text,omitempty" jsonschema_description:"Text to type (type, fill)"`
	Key       string `json:"key,omitempty" jsonschema_description:"Key to press (press) e.g. Enter, Tab, Escape"`
	Direction string `json:"direction,omitempty" jsonschema_description:"Scroll direction: up, down (scroll)"`
	Amount    int    `json:"amount,omitempty" jsonschema_description:"Scroll amount in pixels (scroll, default 300)"`
	Timeout   int    `json:"timeout,omitempty" jsonschema_description:"Wait timeout in ms (waitForText, waitForSelector, default 10000)"`
}

func (backend *Backend) browserInteractHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args BrowserInteractArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "click":
		fields := map[string]any{}
		if args.Ref != "" {
			fields["ref"] = args.Ref
		}
		if args.Selector != "" {
			fields["selector"] = args.Selector
		}
		if args.Ref == "" && args.Selector == "" {
			return mcp.NewToolResultError("ref or selector is required for click"), nil
		}
		result, err := camofoxPost("/tabs/"+args.TabID+"/click", fields)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to click: %v", err)), nil
		}
		return mcpJSONResult(result)

	case "type":
		if args.Ref == "" {
			return mcp.NewToolResultError("ref is required for type"), nil
		}
		result, err := camofoxPost("/tabs/"+args.TabID+"/type", map[string]any{
			"ref":  args.Ref,
			"text": args.Text,
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to type: %v", err)), nil
		}
		return mcpJSONResult(result)

	case "fill":
		// Clear the field first, then type the new value
		if args.Ref == "" && args.Selector == "" {
			return mcp.NewToolResultError("ref or selector is required for fill"), nil
		}

		// Build a JS expression to clear the field
		var clearExpr string
		if args.Selector != "" {
			clearExpr = fmt.Sprintf(`(function(){ var el = document.querySelector('%s'); if(el){ el.value=''; el.dispatchEvent(new Event('input',{bubbles:true})); return true; } return false; })()`, escapeSingleQuoteJS(args.Selector))
		} else {
			// If only ref provided, we need to click then select all + delete
			clearExpr = `true`
		}

		_, err := camofoxPost("/tabs/"+args.TabID+"/evaluate", map[string]any{
			"expression": clearExpr,
		})
		if err != nil && args.Selector != "" {
			return mcp.NewToolResultError(fmt.Sprintf("failed to clear field: %v", err)), nil
		}

		// If using ref, use select-all + delete key approach to clear
		if args.Ref != "" && args.Selector == "" {
			// Triple-click to select all text in the field
			camofoxPost("/tabs/"+args.TabID+"/click", map[string]any{"ref": args.Ref})
			camofoxPost("/tabs/"+args.TabID+"/press", map[string]any{"key": "Control+a"})
			camofoxPost("/tabs/"+args.TabID+"/press", map[string]any{"key": "Delete"})
		}

		// Now type the new value
		typeFields := map[string]any{"text": args.Text}
		if args.Ref != "" {
			typeFields["ref"] = args.Ref
		}
		result, err := camofoxPost("/tabs/"+args.TabID+"/type", typeFields)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to fill: %v", err)), nil
		}
		return mcpJSONResult(result)

	case "press":
		if args.Key == "" {
			return mcp.NewToolResultError("key is required for press"), nil
		}
		result, err := camofoxPost("/tabs/"+args.TabID+"/press", map[string]any{
			"key": args.Key,
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to press key: %v", err)), nil
		}
		return mcpJSONResult(result)

	case "scroll":
		direction := args.Direction
		if direction == "" {
			direction = "down"
		}
		amount := args.Amount
		if amount == 0 {
			amount = 300
		}
		result, err := camofoxPost("/tabs/"+args.TabID+"/scroll", map[string]any{
			"direction": direction,
			"amount":    amount,
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to scroll: %v", err)), nil
		}
		return mcpJSONResult(result)

	case "hover":
		// CamoFox REST API does not have a direct hover endpoint.
		// Dispatch a mouseover event via JS evaluate.
		if args.Ref == "" && args.Selector == "" {
			return mcp.NewToolResultError("ref or selector is required for hover"), nil
		}

		var hoverExpr string
		if args.Selector != "" {
			hoverExpr = fmt.Sprintf(`(function(){ var el = document.querySelector('%s'); if(el){ el.dispatchEvent(new MouseEvent('mouseover',{bubbles:true})); el.dispatchEvent(new MouseEvent('mouseenter',{bubbles:true})); return true; } return false; })()`, escapeSingleQuoteJS(args.Selector))
		} else {
			// With ref, first get snapshot to identify the element, then use
			// a broad approach: click to focus, then dispatch mouseover
			hoverExpr = `(function(){ var el = document.activeElement; if(el){ el.dispatchEvent(new MouseEvent('mouseover',{bubbles:true})); el.dispatchEvent(new MouseEvent('mouseenter',{bubbles:true})); return true; } return false; })()`
			// Click the ref first to focus the element
			_, _ = camofoxPost("/tabs/"+args.TabID+"/click", map[string]any{"ref": args.Ref})
		}

		result, err := camofoxPost("/tabs/"+args.TabID+"/evaluate", map[string]any{
			"expression": hoverExpr,
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to hover: %v", err)), nil
		}
		return mcpJSONResult(map[string]any{
			"success": true,
			"result":  result,
		})

	case "waitForText":
		if args.Text == "" {
			return mcp.NewToolResultError("text is required for waitForText"), nil
		}
		timeout := args.Timeout
		if timeout <= 0 {
			timeout = 10000
		}
		deadline := time.Now().Add(time.Duration(timeout) * time.Millisecond)
		for time.Now().Before(deadline) {
			// Get snapshot and check if text appears
			snapResult, err := camofoxGet("/tabs/" + args.TabID + "/snapshot")
			if err == nil {
				if snapshot, ok := snapResult["snapshot"].(string); ok {
					if strings.Contains(snapshot, args.Text) {
						return mcpJSONResult(map[string]any{
							"found":   true,
							"text":    args.Text,
							"elapsed": time.Since(deadline.Add(-time.Duration(timeout) * time.Millisecond)).Milliseconds(),
						})
					}
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
		return mcp.NewToolResultError(fmt.Sprintf("timeout waiting for text '%s' after %dms", args.Text, timeout)), nil

	case "waitForSelector":
		if args.Selector == "" {
			return mcp.NewToolResultError("selector is required for waitForSelector"), nil
		}
		timeout := args.Timeout
		if timeout <= 0 {
			timeout = 10000
		}
		checkExpr := fmt.Sprintf(`document.querySelector('%s') !== null`, escapeSingleQuoteJS(args.Selector))
		deadline := time.Now().Add(time.Duration(timeout) * time.Millisecond)
		for time.Now().Before(deadline) {
			result, err := camofoxPost("/tabs/"+args.TabID+"/evaluate", map[string]any{
				"expression": checkExpr,
			})
			if err == nil {
				// Check if the result indicates the element was found
				if val, ok := result["result"]; ok {
					if boolVal, ok := val.(bool); ok && boolVal {
						return mcpJSONResult(map[string]any{
							"found":    true,
							"selector": args.Selector,
							"elapsed":  time.Since(deadline.Add(-time.Duration(timeout) * time.Millisecond)).Milliseconds(),
						})
					}
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
		return mcp.NewToolResultError(fmt.Sprintf("timeout waiting for selector '%s' after %dms", args.Selector, timeout)), nil

	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: click, type, fill, press, scroll, hover, waitForText, waitForSelector"), nil
	}
}

// ---------------------------------------------------------------------------
// Tool 3: browserExec -- JS execution + data access
// ---------------------------------------------------------------------------

// BrowserExecArgs is the union argument struct for the browserExec tool.
type BrowserExecArgs struct {
	Action     string           `json:"action" jsonschema:"required" jsonschema_description:"Operation: evaluate, getHtml, getLinks, getCookies, setCookies, getConsole, getErrors"`
	TabID      string           `json:"tabId" jsonschema:"required" jsonschema_description:"Tab ID"`
	Expression string           `json:"expression,omitempty" jsonschema_description:"JavaScript expression (evaluate)"`
	Timeout    int              `json:"timeout,omitempty" jsonschema_description:"Execution timeout in ms (evaluate, default 30000)"`
	Cookies    []map[string]any `json:"cookies,omitempty" jsonschema_description:"Cookies to set [{name, value, domain, path, httpOnly, secure, sameSite}] (setCookies)"`
}

func (backend *Backend) browserExecHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args BrowserExecArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "evaluate":
		if args.Expression == "" {
			return mcp.NewToolResultError("expression is required for evaluate"), nil
		}
		timeout := args.Timeout
		if timeout <= 0 {
			timeout = 30000
		}
		result, err := camofoxPost("/tabs/"+args.TabID+"/evaluate-extended", map[string]any{
			"expression": args.Expression,
			"timeout":    timeout,
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to evaluate: %v", err)), nil
		}
		return mcpJSONResult(result)

	case "getHtml":
		result, err := camofoxPost("/tabs/"+args.TabID+"/evaluate-extended", map[string]any{
			"expression": "document.documentElement.outerHTML",
			"timeout":    30000,
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to get HTML: %v", err)), nil
		}
		return mcpJSONResult(result)

	case "getLinks":
		result, err := camofoxGet("/tabs/" + args.TabID + "/links")
		if err != nil {
			// May return an array
			raw, _, rawErr := camofoxGetRaw("/tabs/" + args.TabID + "/links")
			if rawErr != nil {
				return mcp.NewToolResultError(fmt.Sprintf("failed to get links: %v", err)), nil
			}
			var links any
			if jsonErr := json.Unmarshal(raw, &links); jsonErr != nil {
				return mcp.NewToolResultError(fmt.Sprintf("failed to parse links: %v", jsonErr)), nil
			}
			return mcpJSONResult(map[string]any{"links": links})
		}
		return mcpJSONResult(result)

	case "getCookies":
		raw, _, err := camofoxGetRaw("/tabs/" + args.TabID + "/cookies")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to get cookies: %v", err)), nil
		}
		// CamoFox returns an array of cookie objects
		var cookies any
		if jsonErr := json.Unmarshal(raw, &cookies); jsonErr != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to parse cookies: %v", jsonErr)), nil
		}
		return mcpJSONResult(map[string]any{"cookies": cookies})

	case "setCookies":
		if len(args.Cookies) == 0 {
			return mcp.NewToolResultError("cookies array is required for setCookies"), nil
		}
		body := map[string]any{"cookies": args.Cookies}
		result, err := camofoxRequest("POST", "/sessions/"+camofoxUserID+"/cookies", body)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to set cookies: %v", err)), nil
		}
		return mcpJSONResult(result)

	case "getConsole":
		result, err := camofoxGet("/tabs/" + args.TabID + "/console")
		if err != nil {
			raw, _, rawErr := camofoxGetRaw("/tabs/" + args.TabID + "/console")
			if rawErr != nil {
				return mcp.NewToolResultError(fmt.Sprintf("failed to get console: %v", err)), nil
			}
			var messages any
			if jsonErr := json.Unmarshal(raw, &messages); jsonErr != nil {
				return mcp.NewToolResultError(fmt.Sprintf("failed to parse console: %v", jsonErr)), nil
			}
			return mcpJSONResult(map[string]any{"messages": messages})
		}
		return mcpJSONResult(result)

	case "getErrors":
		result, err := camofoxGet("/tabs/" + args.TabID + "/errors")
		if err != nil {
			raw, _, rawErr := camofoxGetRaw("/tabs/" + args.TabID + "/errors")
			if rawErr != nil {
				return mcp.NewToolResultError(fmt.Sprintf("failed to get errors: %v", err)), nil
			}
			var errors any
			if jsonErr := json.Unmarshal(raw, &errors); jsonErr != nil {
				return mcp.NewToolResultError(fmt.Sprintf("failed to parse errors: %v", jsonErr)), nil
			}
			return mcpJSONResult(map[string]any{"errors": errors})
		}
		return mcpJSONResult(result)

	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: evaluate, getHtml, getLinks, getCookies, setCookies, getConsole, getErrors"), nil
	}
}

// ---------------------------------------------------------------------------
// Tool 4: browserXss -- XSS verification
// ---------------------------------------------------------------------------

// BrowserXssArgs is the union argument struct for the browserXss tool.
type BrowserXssArgs struct {
	Action   string `json:"action" jsonschema:"required" jsonschema_description:"Operation: verifyAlert, injectPayload, testDomSink, checkCsp, disableCsp, disableCors, disableFrameProtection, restoreDefaults"`
	TabID    string `json:"tabId" jsonschema:"required" jsonschema_description:"Tab ID"`
	URL      string `json:"url,omitempty" jsonschema_description:"URL to test (verifyAlert, testDomSink)"`
	Payload  string `json:"payload,omitempty" jsonschema_description:"XSS payload HTML/JS (injectPayload)"`
	Selector string `json:"selector,omitempty" jsonschema_description:"Target element selector (injectPayload)"`
	Sink     string `json:"sink,omitempty" jsonschema_description:"DOM sink to test: location.hash, document.referrer, window.name, postMessage (testDomSink)"`
	Value    string `json:"value,omitempty" jsonschema_description:"Value to inject into sink (testDomSink)"`
}

// xssAlertInterceptorJS returns a JS snippet that overrides alert/confirm/prompt,
// then re-executes all inline scripts to re-trigger any XSS payloads.
// Captured dialog calls are stored in window.__xss_alerts.
const xssAlertInterceptorJS = `(function() {
  window.__xss_alerts = [];
  window.alert = function(msg) { window.__xss_alerts.push({type:'alert',message:String(msg)}); };
  window.confirm = function(msg) { window.__xss_alerts.push({type:'confirm',message:String(msg)}); return false; };
  window.prompt = function(msg,def) { window.__xss_alerts.push({type:'prompt',message:String(msg)}); return null; };
  document.querySelectorAll('script:not([src])').forEach(function(s) {
    try { eval(s.textContent); } catch(e) {}
  });
  return window.__xss_alerts;
})()`

// xssCheckAlertsJS reads back the captured XSS alerts array.
const xssCheckAlertsJS = `(function() { return window.__xss_alerts || []; })()`

func (backend *Backend) browserXssHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args BrowserXssArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "verifyAlert":
		if args.URL == "" {
			return mcp.NewToolResultError("url is required for verifyAlert"), nil
		}

		// 1. Navigate to the URL containing the XSS payload
		_, err := camofoxPost("/tabs/"+args.TabID+"/navigate", map[string]any{"url": args.URL})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to navigate to XSS URL: %v", err)), nil
		}

		// 2. Wait for page to load via readyState poll
		for i := 0; i < 20; i++ {
			time.Sleep(100 * time.Millisecond)
			result, err := camofoxPost("/tabs/"+args.TabID+"/evaluate-extended", map[string]any{
				"expression": "document.readyState",
				"timeout":    2000,
			})
			if err == nil {
				if rs, ok := result["result"].(string); ok && rs == "complete" {
					break
				}
			}
		}

		// 3. Override alert/confirm/prompt and re-execute inline scripts
		alertResult, err := camofoxPost("/tabs/"+args.TabID+"/evaluate-extended", map[string]any{
			"expression": xssAlertInterceptorJS,
			"timeout":    10000,
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to inject alert interceptor: %v", err)), nil
		}

		// 4. Check for captured alerts
		alerts := extractAlerts(alertResult)

		// 5. Also check for event-handler-based XSS by looking for injected elements
		// that may have already fired (img onerror, svg onload, etc.)
		domCheckJS := `(function() {
  var indicators = [];
  var imgs = document.querySelectorAll('img[src]');
  imgs.forEach(function(img) { if(img.naturalWidth === 0) indicators.push({type:'img-error', src:img.src}); });
  var scripts = document.querySelectorAll('script:not([src])');
  scripts.forEach(function(s) { indicators.push({type:'inline-script', content:s.textContent.substring(0,200)}); });
  return {indicators: indicators, alertCount: (window.__xss_alerts||[]).length};
})()`
		domResult, _ := camofoxPost("/tabs/"+args.TabID+"/evaluate-extended", map[string]any{
			"expression": domCheckJS,
			"timeout":    5000,
		})

		// 6. Take screenshot as evidence
		screenshotResult, _ := camofoxGet("/tabs/" + args.TabID + "/screenshot")
		screenshotData := ""
		if screenshotResult != nil {
			if data, ok := screenshotResult["data"].(string); ok {
				screenshotData = data
			}
		}

		verified := len(alerts) > 0
		return mcpJSONResult(map[string]any{
			"verified":      verified,
			"alerts":        alerts,
			"domIndicators": domResult,
			"screenshot":    screenshotData,
			"url":           args.URL,
		})

	case "injectPayload":
		if args.Selector == "" {
			return mcp.NewToolResultError("selector is required for injectPayload"), nil
		}
		if args.Payload == "" {
			return mcp.NewToolResultError("payload is required for injectPayload"), nil
		}

		// 1. Override alert/confirm/prompt first
		camofoxPost("/tabs/"+args.TabID+"/evaluate-extended", map[string]any{
			"expression": `(function() {
  window.__xss_alerts = [];
  window.alert = function(msg) { window.__xss_alerts.push({type:'alert',message:String(msg)}); };
  window.confirm = function(msg) { window.__xss_alerts.push({type:'confirm',message:String(msg)}); return false; };
  window.prompt = function(msg,def) { window.__xss_alerts.push({type:'prompt',message:String(msg)}); return null; };
})()`,
			"timeout": 5000,
		})

		// 2. Inject the payload into the target element
		escapedPayload := escapeJSString(args.Payload)
		injectExpr := fmt.Sprintf(`(function() {
  var el = document.querySelector('%s');
  if (!el) return {success: false, error: 'Element not found'};
  el.innerHTML = %s;
  return {success: true, alerts: window.__xss_alerts};
})()`, escapeSingleQuoteJS(args.Selector), escapedPayload)

		result, err := camofoxPost("/tabs/"+args.TabID+"/evaluate-extended", map[string]any{
			"expression": injectExpr,
			"timeout":    10000,
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to inject payload: %v", err)), nil
		}

		// 3. Wait briefly for event handlers to fire
		time.Sleep(500 * time.Millisecond)

		// 4. Check alerts again
		checkResult, _ := camofoxPost("/tabs/"+args.TabID+"/evaluate-extended", map[string]any{
			"expression": xssCheckAlertsJS,
			"timeout":    5000,
		})
		alerts := extractAlerts(checkResult)

		return mcpJSONResult(map[string]any{
			"success":  true,
			"alerts":   alerts,
			"verified": len(alerts) > 0,
			"result":   result,
		})

	case "testDomSink":
		if args.Sink == "" {
			return mcp.NewToolResultError("sink is required for testDomSink"), nil
		}
		if args.Value == "" {
			return mcp.NewToolResultError("value is required for testDomSink"), nil
		}

		// 1. Override alert/confirm/prompt
		camofoxPost("/tabs/"+args.TabID+"/evaluate-extended", map[string]any{
			"expression": `(function() {
  window.__xss_alerts = [];
  window.alert = function(msg) { window.__xss_alerts.push({type:'alert',message:String(msg)}); };
  window.confirm = function(msg) { window.__xss_alerts.push({type:'confirm',message:String(msg)}); return false; };
  window.prompt = function(msg,def) { window.__xss_alerts.push({type:'prompt',message:String(msg)}); return null; };
})()`,
			"timeout": 5000,
		})

		// 2. Set the sink value
		escapedValue := escapeJSString(args.Value)
		var sinkExpr string
		switch args.Sink {
		case "location.hash":
			sinkExpr = fmt.Sprintf(`location.hash = %s`, escapedValue)
		case "document.referrer":
			// document.referrer is read-only, but we can test by navigating
			// with the value in the referrer
			sinkExpr = fmt.Sprintf(`(function(){ Object.defineProperty(document, 'referrer', {value: %s}); return true; })()`, escapedValue)
		case "window.name":
			sinkExpr = fmt.Sprintf(`window.name = %s`, escapedValue)
		case "postMessage":
			sinkExpr = fmt.Sprintf(`window.postMessage(%s, '*')`, escapedValue)
		default:
			// Allow arbitrary sink expressions
			sinkExpr = fmt.Sprintf(`%s = %s`, args.Sink, escapedValue)
		}

		_, err := camofoxPost("/tabs/"+args.TabID+"/evaluate-extended", map[string]any{
			"expression": sinkExpr,
			"timeout":    5000,
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to set sink value: %v", err)), nil
		}

		// 3. Wait for JS execution
		time.Sleep(1 * time.Second)

		// 4. Check alerts
		checkResult, _ := camofoxPost("/tabs/"+args.TabID+"/evaluate-extended", map[string]any{
			"expression": xssCheckAlertsJS,
			"timeout":    5000,
		})
		alerts := extractAlerts(checkResult)

		return mcpJSONResult(map[string]any{
			"sink":     args.Sink,
			"value":    args.Value,
			"alerts":   alerts,
			"verified": len(alerts) > 0,
		})

	case "checkCsp":
		// 1. Read CSP from meta tags (response headers are not accessible from JS)
		cspCheckJS := `(function() {
  var result = {metaCsp: null, headerNote: 'CSP response headers cannot be read from JavaScript; check the HTTP response headers separately'};
  var meta = document.querySelector('meta[http-equiv="Content-Security-Policy"]');
  if (meta) result.metaCsp = meta.content;
  var metaReport = document.querySelector('meta[http-equiv="Content-Security-Policy-Report-Only"]');
  if (metaReport) result.metaCspReportOnly = metaReport.content;
  return result;
})()`

		cspResult, err := camofoxPost("/tabs/"+args.TabID+"/evaluate-extended", map[string]any{
			"expression": cspCheckJS,
			"timeout":    5000,
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to check CSP: %v", err)), nil
		}

		// 2. Parse and analyze the CSP if found
		analysis := analyzeCsp(cspResult)

		return mcpJSONResult(map[string]any{
			"csp":      cspResult,
			"analysis": analysis,
		})

	case "disableCsp":
		// Disable CSP by overriding fetch/XHR to remove CSP headers client-side,
		// and removing any CSP meta tags from the DOM.
		disableCSPJS := `(function() {
			// Remove CSP meta tags
			document.querySelectorAll('meta[http-equiv="Content-Security-Policy"]').forEach(function(m) { m.remove(); });
			document.querySelectorAll('meta[http-equiv="Content-Security-Policy-Report-Only"]').forEach(function(m) { m.remove(); });

			// Mark CSP as disabled for this page
			window.__lorg_csp_disabled = true;
			return {disabled: true, removedMetaTags: true, note: 'CSP meta tags removed. HTTP CSP headers still apply to initial load but not to dynamically injected scripts. Use eval() or innerHTML to test payloads.'};
		})()`

		evalResult, err := camofoxPost("/tabs/"+args.TabID+"/evaluate", map[string]any{"expression": disableCSPJS})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("disableCsp failed: %v", err)), nil
		}
		return mcpJSONResult(map[string]any{
			"success": true,
			"action":  "disableCsp",
			"result":  evalResult["result"],
			"note":    "CSP meta tags removed. For full CSP bypass, inject scripts via eval() in the evaluate tool. HTTP-delivered CSP still applies to new page loads.",
		})

	case "disableCors":
		// Can't fully disable CORS from client-side JS (it's enforced by the browser engine).
		// But we can: 1) use lorg proxy to strip CORS headers on responses,
		// 2) override fetch to use no-cors mode, 3) document the limitation.
		disableCorsJS := `(function() {
			// Override fetch to use no-cors mode and ignore CORS errors
			var originalFetch = window.fetch;
			window.fetch = function(url, opts) {
				opts = opts || {};
				// Don't force no-cors if cors was explicitly set — just catch errors
				return originalFetch.call(this, url, opts).catch(function(err) {
					if (err.message && err.message.indexOf('CORS') !== -1) {
						console.warn('[lorg] CORS blocked: ' + url + '. Use lorg sendHttpRequest for cross-origin requests.');
					}
					throw err;
				});
			};
			window.__lorg_cors_override = true;
			return {
				override: true,
				note: 'CORS is browser-enforced and cannot be fully disabled from JS. For cross-origin testing, use lorg sendHttpRequest (bypasses browser CORS) or route through lorg proxy with CORS header stripping.'
			};
		})()`

		evalResult, err := camofoxPost("/tabs/"+args.TabID+"/evaluate", map[string]any{"expression": disableCorsJS})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("disableCors failed: %v", err)), nil
		}
		return mcpJSONResult(map[string]any{
			"success": true,
			"action":  "disableCors",
			"result":  evalResult["result"],
			"note":    "CORS is browser-enforced. For true cross-origin requests, use lorg sendHttpRequest which bypasses the browser entirely. Fetch override installed to log CORS blocks.",
		})

	case "disableFrameProtection":
		// Remove X-Frame-Options and frame-ancestors CSP that prevent framing.
		// This enables clickjacking testing.
		disableFrameJS := `(function() {
			// Remove frame-busting scripts
			document.querySelectorAll('script').forEach(function(s) {
				if (s.textContent && (s.textContent.indexOf('top.location') !== -1 || s.textContent.indexOf('self !== top') !== -1 || s.textContent.indexOf('parent.frames') !== -1)) {
					s.remove();
				}
			});
			// Remove CSP meta tags with frame-ancestors
			document.querySelectorAll('meta[http-equiv="Content-Security-Policy"]').forEach(function(m) {
				if (m.content && m.content.indexOf('frame-ancestors') !== -1) {
					m.remove();
				}
			});
			// Override top/parent references to prevent frame-busting
			try {
				Object.defineProperty(window, 'top', {get: function() { return window; }});
			} catch(e) {}
			window.__lorg_frame_protection_disabled = true;
			return {disabled: true, note: 'Frame-busting scripts removed, top reference overridden. X-Frame-Options HTTP header still applies to new navigations — test framing from an attacker-controlled page.'};
		})()`

		evalResult, err := camofoxPost("/tabs/"+args.TabID+"/evaluate", map[string]any{"expression": disableFrameJS})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("disableFrameProtection failed: %v", err)), nil
		}
		return mcpJSONResult(map[string]any{
			"success": true,
			"action":  "disableFrameProtection",
			"result":  evalResult["result"],
		})

	case "restoreDefaults":
		// Reload the page to restore all default browser security settings
		_, err := camofoxPost("/tabs/"+args.TabID+"/refresh", nil)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("restoreDefaults failed: %v", err)), nil
		}
		for i := 0; i < 20; i++ {
			time.Sleep(100 * time.Millisecond)
			result, err := camofoxPost("/tabs/"+args.TabID+"/evaluate-extended", map[string]any{
				"expression": "document.readyState",
				"timeout":    2000,
			})
			if err == nil {
				if rs, ok := result["result"].(string); ok && rs == "complete" {
					break
				}
			}
		}
		return mcpJSONResult(map[string]any{
			"success": true,
			"action":  "restoreDefaults",
			"note":    "Page reloaded. All JS overrides cleared. Browser security defaults restored.",
		})

	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: verifyAlert, injectPayload, testDomSink, checkCsp, disableCsp, disableCors, disableFrameProtection, restoreDefaults"), nil
	}
}

// ---------------------------------------------------------------------------
// Tool 5: browserAuth -- authentication & session management
// ---------------------------------------------------------------------------

// BrowserAuthArgs is the union argument struct for the browserAuth tool.
type BrowserAuthArgs struct {
	Action      string           `json:"action" jsonschema:"required" jsonschema_description:"Operation: login, importCookies, exportCookies"`
	TabID       string           `json:"tabId,omitempty" jsonschema_description:"Tab ID"`
	URL         string           `json:"url,omitempty" jsonschema_description:"Login page URL (login)"`
	Username    string           `json:"username,omitempty" jsonschema_description:"Username (login)"`
	Password    string           `json:"password,omitempty" jsonschema_description:"Password (login)"`
	UsernameRef string           `json:"usernameRef,omitempty" jsonschema_description:"Ref or selector for username field (login)"`
	PasswordRef string           `json:"passwordRef,omitempty" jsonschema_description:"Ref or selector for password field (login)"`
	SubmitRef   string           `json:"submitRef,omitempty" jsonschema_description:"Ref or selector for submit button (login)"`
	Cookies     []map[string]any `json:"cookies,omitempty" jsonschema_description:"Cookies to import [{name,value,domain,path}] (importCookies)"`
}

func (backend *Backend) browserAuthHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args BrowserAuthArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "login":
		if args.TabID == "" {
			return mcp.NewToolResultError("tabId is required for login"), nil
		}
		if args.URL == "" {
			return mcp.NewToolResultError("url is required for login"), nil
		}

		// 1. Navigate to the login page
		_, err := camofoxPost("/tabs/"+args.TabID+"/navigate", map[string]any{"url": args.URL})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to navigate to login page: %v", err)), nil
		}
		for i := 0; i < 20; i++ {
			time.Sleep(100 * time.Millisecond)
			result, err := camofoxPost("/tabs/"+args.TabID+"/evaluate-extended", map[string]any{
				"expression": "document.readyState",
				"timeout":    2000,
			})
			if err == nil {
				if rs, ok := result["result"].(string); ok && rs == "complete" {
					break
				}
			}
		}

		// 2. If no refs provided, auto-detect form fields
		usernameRef := args.UsernameRef
		passwordRef := args.PasswordRef
		submitRef := args.SubmitRef

		if usernameRef == "" || passwordRef == "" || submitRef == "" {
			detectJS := `(function() {
  var inputs = document.querySelectorAll('input');
  var userField = null, passField = null, submitBtn = null;
  inputs.forEach(function(inp) {
    var t = (inp.type || '').toLowerCase();
    var n = (inp.name || '').toLowerCase();
    var id = (inp.id || '').toLowerCase();
    var ph = (inp.placeholder || '').toLowerCase();
    if (t === 'password') passField = inp;
    else if (t === 'email' || t === 'text' || n.match(/user|email|login|account/) || id.match(/user|email|login|account/) || ph.match(/user|email|login/)) {
      if (!userField) userField = inp;
    }
  });
  submitBtn = document.querySelector('button[type="submit"], input[type="submit"], button.login, button.signin, button[name="login"]');
  if (!submitBtn) submitBtn = document.querySelector('form button');
  return {
    username: userField ? { selector: userField.id ? '#'+userField.id : (userField.name ? 'input[name="'+userField.name+'"]' : null) } : null,
    password: passField ? { selector: passField.id ? '#'+passField.id : (passField.name ? 'input[name="'+passField.name+'"]' : null) } : null,
    submit: submitBtn ? { selector: submitBtn.id ? '#'+submitBtn.id : (submitBtn.name ? 'button[name="'+submitBtn.name+'"]' : (submitBtn.type === 'submit' ? 'button[type="submit"]' : null)) } : null
  };
})()`

			detectResult, detectErr := camofoxPost("/tabs/"+args.TabID+"/evaluate-extended", map[string]any{
				"expression": detectJS,
				"timeout":    5000,
			})
			if detectErr == nil && detectResult != nil {
				if result, ok := detectResult["result"].(map[string]any); ok {
					if usernameRef == "" {
						if u, ok := result["username"].(map[string]any); ok {
							if sel, ok := u["selector"].(string); ok && sel != "" {
								usernameRef = sel
							}
						}
					}
					if passwordRef == "" {
						if p, ok := result["password"].(map[string]any); ok {
							if sel, ok := p["selector"].(string); ok && sel != "" {
								passwordRef = sel
							}
						}
					}
					if submitRef == "" {
						if s, ok := result["submit"].(map[string]any); ok {
							if sel, ok := s["selector"].(string); ok && sel != "" {
								submitRef = sel
							}
						}
					}
				}
			}
		}

		if usernameRef == "" {
			return mcp.NewToolResultError("could not detect username field; provide usernameRef"), nil
		}
		if passwordRef == "" {
			return mcp.NewToolResultError("could not detect password field; provide passwordRef"), nil
		}

		// 3. Type username -- use selector-based approach via evaluate for reliability
		typeUsernameJS := fmt.Sprintf(`(function() {
  var el = document.querySelector('%s');
  if (!el) return false;
  el.focus();
  el.value = '%s';
  el.dispatchEvent(new Event('input', {bubbles:true}));
  el.dispatchEvent(new Event('change', {bubbles:true}));
  return true;
})()`, escapeSingleQuoteJS(usernameRef), escapeSingleQuoteJS(args.Username))
		camofoxPost("/tabs/"+args.TabID+"/evaluate-extended", map[string]any{
			"expression": typeUsernameJS,
			"timeout":    5000,
		})

		// 4. Type password
		typePasswordJS := fmt.Sprintf(`(function() {
  var el = document.querySelector('%s');
  if (!el) return false;
  el.focus();
  el.value = '%s';
  el.dispatchEvent(new Event('input', {bubbles:true}));
  el.dispatchEvent(new Event('change', {bubbles:true}));
  return true;
})()`, escapeSingleQuoteJS(passwordRef), escapeSingleQuoteJS(args.Password))
		camofoxPost("/tabs/"+args.TabID+"/evaluate-extended", map[string]any{
			"expression": typePasswordJS,
			"timeout":    5000,
		})

		// 5. Click submit
		if submitRef != "" {
			submitJS := fmt.Sprintf(`(function() {
  var el = document.querySelector('%s');
  if (!el) { var form = document.querySelector('form'); if(form) { form.submit(); return true; } return false; }
  el.click();
  return true;
})()`, escapeSingleQuoteJS(submitRef))
			camofoxPost("/tabs/"+args.TabID+"/evaluate-extended", map[string]any{
				"expression": submitJS,
				"timeout":    5000,
			})
		} else {
			// Fallback: submit the first form
			camofoxPost("/tabs/"+args.TabID+"/evaluate-extended", map[string]any{
				"expression": `(function() { var f = document.querySelector('form'); if(f) { f.submit(); return true; } return false; })()`,
				"timeout":    5000,
			})
		}

		// 6. Wait for navigation/redirect
		time.Sleep(3 * time.Second)

		// 7. Get the final URL and snapshot
		snapshot, _ := camofoxGet("/tabs/" + args.TabID + "/snapshot")
		finalURL := ""
		snapshotText := ""
		if snapshot != nil {
			if u, ok := snapshot["url"].(string); ok {
				finalURL = u
			}
			if s, ok := snapshot["snapshot"].(string); ok {
				snapshotText = s
			}
		}

		// 8. Export cookies
		cookieRaw, _, _ := camofoxGetRaw("/tabs/" + args.TabID + "/cookies")
		var cookies any
		if cookieRaw != nil {
			json.Unmarshal(cookieRaw, &cookies)
		}

		return mcpJSONResult(map[string]any{
			"success":  true,
			"url":      finalURL,
			"snapshot": snapshotText,
			"cookies":  cookies,
			"fields": map[string]any{
				"usernameRef": usernameRef,
				"passwordRef": passwordRef,
				"submitRef":   submitRef,
			},
		})

	case "importCookies":
		if len(args.Cookies) == 0 {
			return mcp.NewToolResultError("cookies array is required for importCookies"), nil
		}
		body := map[string]any{"cookies": args.Cookies}
		result, err := camofoxRequest("POST", "/sessions/"+camofoxUserID+"/cookies", body)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to import cookies: %v", err)), nil
		}
		return mcpJSONResult(map[string]any{
			"success": true,
			"result":  result,
			"count":   len(args.Cookies),
		})

	case "exportCookies":
		if args.TabID == "" {
			return mcp.NewToolResultError("tabId is required for exportCookies"), nil
		}
		raw, _, err := camofoxGetRaw("/tabs/" + args.TabID + "/cookies")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to export cookies: %v", err)), nil
		}
		var cookies any
		if jsonErr := json.Unmarshal(raw, &cookies); jsonErr != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to parse cookies: %v", jsonErr)), nil
		}
		return mcpJSONResult(map[string]any{
			"cookies": cookies,
		})

	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: login, importCookies, exportCookies"), nil
	}
}

// ---------------------------------------------------------------------------
// Tool 6: browserConfig -- server configuration
// ---------------------------------------------------------------------------

// BrowserConfigArgs is the union argument struct for the browserConfig tool.
type BrowserConfigArgs struct {
	Action     string `json:"action" jsonschema:"required" jsonschema_description:"Operation: status, setDisplay, setCamofoxUrl"`
	Headless   string `json:"headless,omitempty" jsonschema_description:"Display mode: true (headless), false (headed), virtual (VNC) (setDisplay)"`
	CamofoxUrl string `json:"camofoxUrl,omitempty" jsonschema_description:"CamoFox server URL e.g. http://localhost:9377 (setCamofoxUrl)"`
}

func (backend *Backend) browserConfigHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args BrowserConfigArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "status":
		result, err := camofoxRequest("GET", "/health", nil)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("CamoFox server not reachable: %v", err)), nil
		}
		result["camofoxUrl"] = camofoxURL
		result["userId"] = camofoxUserID
		return mcpJSONResult(result)

	case "setDisplay":
		if args.Headless == "" {
			return mcp.NewToolResultError("headless is required for setDisplay (true, false, or virtual)"), nil
		}

		var headlessVal any
		switch args.Headless {
		case "true":
			headlessVal = true
		case "false":
			headlessVal = false
		case "virtual":
			headlessVal = "virtual"
		default:
			return mcp.NewToolResultError("headless must be 'true', 'false', or 'virtual'"), nil
		}

		result, err := camofoxPost("/sessions/"+camofoxUserID+"/toggle-display", map[string]any{
			"headless": headlessVal,
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to set display mode: %v", err)), nil
		}
		return mcpJSONResult(map[string]any{
			"success":  true,
			"headless": headlessVal,
			"result":   result,
		})

	case "setCamofoxUrl":
		if args.CamofoxUrl == "" {
			return mcp.NewToolResultError("camofoxUrl is required for setCamofoxUrl"), nil
		}
		oldURL := camofoxURL
		camofoxURL = strings.TrimRight(args.CamofoxUrl, "/")
		return mcpJSONResult(map[string]any{
			"success":     true,
			"previousUrl": oldURL,
			"currentUrl":  camofoxURL,
		})

	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: status, setDisplay, setCamofoxUrl"), nil
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// escapeSingleQuoteJS escapes single quotes and backslashes in a string
// for safe embedding inside a JS single-quoted string literal.
func escapeSingleQuoteJS(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\r`)
	return s
}

// escapeJSString wraps a Go string as a JSON-encoded JS string literal,
// which is safe for any content (handles quotes, newlines, unicode, etc.).
func escapeJSString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		// Fallback: single-quoted with basic escaping
		return "'" + escapeSingleQuoteJS(s) + "'"
	}
	return string(b) // Returns a double-quoted JSON string, valid in JS
}

// extractAlerts pulls the XSS alerts array from a CamoFox evaluate result.
// CamoFox wraps the JS return value under a "result" key.
func extractAlerts(evalResult map[string]any) []any {
	if evalResult == nil {
		return nil
	}

	// The evaluate-extended endpoint returns {result: <value>}
	resultVal, ok := evalResult["result"]
	if !ok {
		// Try the raw value if no wrapping
		resultVal = evalResult
	}

	switch v := resultVal.(type) {
	case []any:
		return v
	case map[string]any:
		// May be wrapped differently; check for an array inside
		if alerts, ok := v["alerts"].([]any); ok {
			return alerts
		}
	}

	return nil
}

// analyzeCsp performs basic Content Security Policy analysis, flagging
// common bypass conditions that are useful during XSS testing.
func analyzeCsp(cspResult map[string]any) map[string]any {
	analysis := map[string]any{
		"bypasses": []string{},
		"missing":  []string{},
		"warnings": []string{},
	}

	if cspResult == nil {
		analysis["missing"] = []string{"no CSP detected (neither meta tag nor headers checked)"}
		return analysis
	}

	// Extract the CSP string from the result
	var cspString string
	if result, ok := cspResult["result"].(map[string]any); ok {
		if metaCsp, ok := result["metaCsp"].(string); ok {
			cspString = metaCsp
		}
	}

	if cspString == "" {
		analysis["missing"] = []string{"no CSP meta tag found; check HTTP response headers for Content-Security-Policy header"}
		return analysis
	}

	analysis["raw"] = cspString

	var bypasses []string
	var missing []string
	var warnings []string

	directives := parseCspDirectives(cspString)

	// Check for unsafe-inline
	if containsCspValue(directives, "script-src", "'unsafe-inline'") || containsCspValue(directives, "default-src", "'unsafe-inline'") {
		bypasses = append(bypasses, "unsafe-inline in script-src allows inline script execution")
	}

	// Check for unsafe-eval
	if containsCspValue(directives, "script-src", "'unsafe-eval'") || containsCspValue(directives, "default-src", "'unsafe-eval'") {
		bypasses = append(bypasses, "unsafe-eval in script-src allows eval() and similar")
	}

	// Check for wildcards
	if containsCspValue(directives, "script-src", "*") || containsCspValue(directives, "default-src", "*") {
		bypasses = append(bypasses, "wildcard (*) in script-src allows loading scripts from any origin")
	}

	// Check for data: in script-src
	if containsCspValue(directives, "script-src", "data:") || containsCspValue(directives, "default-src", "data:") {
		bypasses = append(bypasses, "data: URI in script-src allows inline script via data:text/html")
	}

	// Check for blob: in script-src
	if containsCspValue(directives, "script-src", "blob:") {
		bypasses = append(bypasses, "blob: URI in script-src allows script loading via Blob URLs")
	}

	// Check for missing directives
	if _, ok := directives["script-src"]; !ok {
		if _, ok := directives["default-src"]; !ok {
			missing = append(missing, "no script-src or default-src directive")
		}
	}
	if _, ok := directives["object-src"]; !ok {
		if _, ok := directives["default-src"]; !ok {
			missing = append(missing, "no object-src directive (allows plugin-based XSS)")
		}
	}
	if _, ok := directives["base-uri"]; !ok {
		missing = append(missing, "no base-uri directive (allows base tag injection)")
	}
	if _, ok := directives["frame-ancestors"]; !ok {
		warnings = append(warnings, "no frame-ancestors directive (clickjacking possible)")
	}

	// Check for overly permissive hosts
	for _, dir := range []string{"script-src", "default-src"} {
		if vals, ok := directives[dir]; ok {
			for _, v := range vals {
				if strings.HasSuffix(v, ".googleapis.com") || strings.HasSuffix(v, ".cloudflare.com") || strings.HasSuffix(v, ".jsdelivr.net") {
					warnings = append(warnings, fmt.Sprintf("CDN host '%s' in %s may host attacker-controlled scripts", v, dir))
				}
			}
		}
	}

	analysis["bypasses"] = bypasses
	analysis["missing"] = missing
	analysis["warnings"] = warnings
	analysis["directives"] = directives

	return analysis
}

// parseCspDirectives splits a CSP policy string into a map of directive name to values.
func parseCspDirectives(csp string) map[string][]string {
	directives := make(map[string][]string)
	parts := strings.Split(csp, ";")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		tokens := strings.Fields(part)
		if len(tokens) == 0 {
			continue
		}
		name := strings.ToLower(tokens[0])
		directives[name] = tokens[1:]
	}
	return directives
}

// containsCspValue checks if a CSP directive contains a specific value.
func containsCspValue(directives map[string][]string, directive, value string) bool {
	vals, ok := directives[directive]
	if !ok {
		return false
	}
	for _, v := range vals {
		if strings.EqualFold(v, value) {
			return true
		}
	}
	return false
}
