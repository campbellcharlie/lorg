package app

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/glitchedgitz/pocketbase/models"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/pocketbase/dbx"
)

// ---------------------------------------------------------------------------
// Input schemas
// ---------------------------------------------------------------------------

type TemplateRegisterArgs struct {
	Name            string            `json:"name" jsonschema:"required" jsonschema_description:"Unique template name"`
	TLS             bool              `json:"tls" jsonschema:"required" jsonschema_description:"Use HTTPS"`
	Host            string            `json:"host" jsonschema:"required" jsonschema_description:"Target hostname"`
	Port            int               `json:"port" jsonschema:"required" jsonschema_description:"Target port"`
	HttpVersion     int               `json:"httpVersion" jsonschema:"required" jsonschema_description:"HTTP version: 1 or 2"`
	RequestTemplate string            `json:"requestTemplate" jsonschema:"required" jsonschema_description:"Raw HTTP request with ${VAR} placeholders"`
	Variables       map[string]string `json:"variables,omitempty" jsonschema_description:"Default variable values"`
	Description     string            `json:"description,omitempty" jsonschema_description:"Template description"`
	InjectSession   bool              `json:"injectSession,omitempty" jsonschema_description:"Auto-inject active session cookies/headers"`
	JsonEscapeVars  bool              `json:"jsonEscapeVars,omitempty" jsonschema_description:"JSON-escape variable values before substitution"`
	ExtractRegex    string            `json:"extractRegex,omitempty" jsonschema_description:"Regex to extract from response"`
	ExtractGroup    int               `json:"extractGroup,omitempty" jsonschema_description:"Capture group for extraction (default: 1)"`
}

type TemplateSendFromArgs struct {
	TemplateName string            `json:"templateName" jsonschema:"required" jsonschema_description:"Template name to use"`
	Variables    map[string]string `json:"variables,omitempty" jsonschema_description:"Variable substitutions (overrides defaults)"`
	Note         string            `json:"note,omitempty" jsonschema_description:"Note to attach to request"`
}

type TemplateSendBatchArgs struct {
	TemplateName string              `json:"templateName" jsonschema:"required" jsonschema_description:"Template name"`
	VariableSets []map[string]string `json:"variableSets" jsonschema:"required" jsonschema_description:"Array of variable sets to send"`
	Note         string              `json:"note,omitempty" jsonschema_description:"Note to attach"`
}

type TemplateSequenceStep struct {
	TemplateName string            `json:"templateName" jsonschema:"required" jsonschema_description:"Template name for this step"`
	Variables    map[string]string `json:"variables,omitempty" jsonschema_description:"Variable substitutions"`
	Note         string            `json:"note,omitempty" jsonschema_description:"Note for this step"`
}

type TemplateSendSequenceArgs struct {
	Steps []TemplateSequenceStep `json:"steps" jsonschema:"required" jsonschema_description:"Ordered steps to execute"`
}

type TemplateListArgs struct{}

type TemplateDeleteArgs struct {
	Name string `json:"name" jsonschema:"required" jsonschema_description:"Template name to delete"`
}

// ---------------------------------------------------------------------------
// Tool handlers
// ---------------------------------------------------------------------------

func (backend *Backend) templateRegisterHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args TemplateRegisterArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	dao := backend.App.Dao()

	// Check if a template with this name already exists
	existing, _ := dao.FindFirstRecordByFilter("_mcp_templates", "name = {:name}", dbx.Params{"name": args.Name})
	if existing != nil {
		return mcp.NewToolResultError(fmt.Sprintf("template with name %q already exists", args.Name)), nil
	}

	collection, err := dao.FindCollectionByNameOrId("_mcp_templates")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to find _mcp_templates collection: %v", err)), nil
	}

	record := models.NewRecord(collection)
	record.Set("name", args.Name)
	record.Set("tls", args.TLS)
	record.Set("host", args.Host)
	record.Set("port", args.Port)
	record.Set("http_version", args.HttpVersion)
	record.Set("request_template", args.RequestTemplate)
	record.Set("variables", args.Variables)
	record.Set("description", args.Description)
	record.Set("inject_session", args.InjectSession)
	record.Set("json_escape_vars", args.JsonEscapeVars)
	record.Set("extract_regex", args.ExtractRegex)
	record.Set("extract_group", args.ExtractGroup)

	if err := dao.SaveRecord(record); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to save template: %v", err)), nil
	}

	return mcpJSONResult(map[string]any{
		"success":    true,
		"name":       args.Name,
		"templateId": record.Id,
	})
}

func (backend *Backend) templateSendFromHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args TemplateSendFromArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	dao := backend.App.Dao()

	tmplRecord, err := dao.FindFirstRecordByFilter("_mcp_templates", "name = {:name}", dbx.Params{"name": args.TemplateName})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("template not found: %s", args.TemplateName)), nil
	}

	resp, extracted, err := backend.executeTemplate(tmplRecord, args.Variables, args.Note)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	result := map[string]any{
		"response": resp.Response,
		"time":     resp.Time,
		"userdata": resp.UserData,
	}
	if extracted != "" {
		result["extracted"] = extracted
	}

	return mcpJSONResult(result)
}

func (backend *Backend) templateSendBatchHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args TemplateSendBatchArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	dao := backend.App.Dao()

	tmplRecord, err := dao.FindFirstRecordByFilter("_mcp_templates", "name = {:name}", dbx.Params{"name": args.TemplateName})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("template not found: %s", args.TemplateName)), nil
	}

	// Cap at 50 variable sets
	varSets := args.VariableSets
	if len(varSets) > 50 {
		varSets = varSets[:50]
	}

	results := make([]map[string]any, 0, len(varSets))
	successCount := 0

	for i, varSet := range varSets {
		entry := map[string]any{
			"index": i,
		}

		resp, extracted, err := backend.executeTemplate(tmplRecord, varSet, args.Note)
		if err != nil {
			entry["error"] = err.Error()
		} else {
			entry["status"] = "ok"
			entry["time"] = resp.Time
			if extracted != "" {
				entry["extracted"] = extracted
			}
			successCount++
		}

		results = append(results, entry)
	}

	return mcpJSONResult(map[string]any{
		"results":      results,
		"total":        len(varSets),
		"successCount": successCount,
	})
}

func (backend *Backend) templateSendSequenceHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args TemplateSendSequenceArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	dao := backend.App.Dao()

	results := make([]map[string]any, 0, len(args.Steps))
	prevExtract := ""

	for i, step := range args.Steps {
		entry := map[string]any{
			"step":         i,
			"templateName": step.TemplateName,
		}

		tmplRecord, err := dao.FindFirstRecordByFilter("_mcp_templates", "name = {:name}", dbx.Params{"name": step.TemplateName})
		if err != nil {
			entry["error"] = fmt.Sprintf("template not found: %s", step.TemplateName)
			results = append(results, entry)
			continue
		}

		// Merge the previous extraction into this step's variables
		vars := step.Variables
		if vars == nil {
			vars = map[string]string{}
		}
		if prevExtract != "" {
			vars["PREV_EXTRACT"] = prevExtract
		}

		resp, extracted, err := backend.executeTemplate(tmplRecord, vars, step.Note)
		if err != nil {
			entry["error"] = err.Error()
		} else {
			entry["status"] = "ok"
			entry["time"] = resp.Time
			if extracted != "" {
				entry["extracted"] = extracted
				prevExtract = extracted
			}
		}

		results = append(results, entry)
	}

	return mcpJSONResult(map[string]any{
		"results": results,
		"total":   len(args.Steps),
	})
}

func (backend *Backend) templateListHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	dao := backend.App.Dao()

	records, err := dao.FindRecordsByExpr("_mcp_templates")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to list templates: %v", err)), nil
	}

	templates := make([]map[string]any, 0, len(records))
	for _, rec := range records {
		varCount := 0
		if raw := rec.Get("variables"); raw != nil {
			if m, ok := raw.(map[string]any); ok {
				varCount = len(m)
			}
		}

		templates = append(templates, map[string]any{
			"name":          rec.GetString("name"),
			"host":          rec.GetString("host"),
			"port":          rec.GetFloat("port"),
			"tls":           rec.GetBool("tls"),
			"description":   rec.GetString("description"),
			"variableCount": varCount,
		})
	}

	return mcpJSONResult(map[string]any{
		"templates": templates,
		"count":     len(templates),
	})
}

func (backend *Backend) templateDeleteHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args TemplateDeleteArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	dao := backend.App.Dao()

	record, err := dao.FindFirstRecordByFilter("_mcp_templates", "name = {:name}", dbx.Params{"name": args.Name})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("template not found: %s", args.Name)), nil
	}

	if err := dao.DeleteRecord(record); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to delete template: %v", err)), nil
	}

	return mcpJSONResult(map[string]any{
		"success": true,
		"deleted": args.Name,
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// executeTemplate loads default variables from a template record, merges with
// overrides, substitutes placeholders, optionally injects session data, sends
// the request, and optionally extracts a value from the response.
func (backend *Backend) executeTemplate(tmplRecord *models.Record, overrideVars map[string]string, note string) (*RepeaterSendResponse, string, error) {
	// Load default variables from template
	defaultVars := map[string]string{}
	if raw := tmplRecord.Get("variables"); raw != nil {
		if m, ok := raw.(map[string]any); ok {
			for k, v := range m {
				defaultVars[k] = fmt.Sprintf("%v", v)
			}
		}
	}

	// Merge: provided variables override defaults
	merged := make(map[string]string, len(defaultVars)+len(overrideVars))
	for k, v := range defaultVars {
		merged[k] = v
	}
	for k, v := range overrideVars {
		merged[k] = v
	}

	jsonEscape := tmplRecord.GetBool("json_escape_vars")
	rawRequest := substituteVariables(tmplRecord.GetString("request_template"), merged, jsonEscape)

	// Normalize line endings: ensure \r\n (CRLF) for HTTP compliance
	rawRequest = strings.ReplaceAll(rawRequest, "\r\n", "\n")
	rawRequest = strings.ReplaceAll(rawRequest, "\n", "\r\n")

	// Inject session cookies/headers if enabled
	if tmplRecord.GetBool("inject_session") {
		injected, err := backend.injectSessionIntoRequest(rawRequest)
		if err == nil {
			rawRequest = injected
		}
	}

	host := tmplRecord.GetString("host")
	port := int(tmplRecord.GetFloat("port"))
	tls := tmplRecord.GetBool("tls")
	httpVersion := int(tmplRecord.GetFloat("http_version"))

	resp, err := backend.sendRepeaterLogic(&RepeaterSendRequest{
		Host:        host,
		Port:        fmt.Sprintf("%d", port),
		TLS:         tls,
		Request:     rawRequest,
		Timeout:     30,
		HTTP2:       httpVersion == 2,
		Url:         fmt.Sprintf("%s://%s:%d", map[bool]string{true: "https", false: "http"}[tls], host, port),
		Note:        note,
		GeneratedBy: "ai/mcp/template",
	})
	if err != nil {
		return nil, "", err
	}

	// Extract value from response if extract_regex is configured
	extracted := ""
	extractRegex := tmplRecord.GetString("extract_regex")
	if extractRegex != "" && resp.Response != "" {
		re, err := regexp.Compile(extractRegex)
		if err == nil {
			matches := re.FindStringSubmatch(resp.Response)
			group := int(tmplRecord.GetFloat("extract_group"))
			if group <= 0 {
				group = 1
			}
			if len(matches) > group {
				extracted = matches[group]
			}
		}
	}

	return resp, extracted, nil
}

// substituteVariables replaces all ${VAR} placeholders in the template string
// with the corresponding variable values. When jsonEscape is true, values are
// JSON-escaped before substitution.
func substituteVariables(template string, variables map[string]string, jsonEscape bool) string {
	result := template
	for key, value := range variables {
		placeholder := "${" + key + "}"
		v := value
		if jsonEscape {
			escaped, _ := json.Marshal(v)
			// Remove surrounding quotes added by json.Marshal
			v = string(escaped[1 : len(escaped)-1])
		}
		result = strings.ReplaceAll(result, placeholder, v)
	}
	return result
}

// injectSessionIntoRequest finds the active session and injects its cookies
// and custom headers into the raw HTTP request. If no active session exists,
// the request is returned unmodified.
func (backend *Backend) injectSessionIntoRequest(rawRequest string) (string, error) {
	session, err := backend.findActiveSession()
	if err != nil {
		// No active session -- return request as-is
		return rawRequest, nil
	}

	// Inject Cookie header
	cookiesRaw := session.Get("cookies")
	if cookies, ok := cookiesRaw.(map[string]any); ok && len(cookies) > 0 {
		cookieParts := make([]string, 0, len(cookies))
		for k, v := range cookies {
			cookieParts = append(cookieParts, fmt.Sprintf("%s=%v", k, v))
		}
		cookieHeader := "Cookie: " + strings.Join(cookieParts, "; ")

		// Insert after the first line (the request line)
		lines := strings.SplitN(rawRequest, "\r\n", 2)
		if len(lines) == 2 {
			rawRequest = lines[0] + "\r\n" + cookieHeader + "\r\n" + lines[1]
		}
	}

	// Inject custom headers
	headersRaw := session.Get("headers")
	if headers, ok := headersRaw.(map[string]any); ok {
		for k, v := range headers {
			header := fmt.Sprintf("%s: %v", k, v)
			lines := strings.SplitN(rawRequest, "\r\n", 2)
			if len(lines) == 2 {
				rawRequest = lines[0] + "\r\n" + header + "\r\n" + lines[1]
			}
		}
	}

	return rawRequest, nil
}

// findActiveSession is defined in mcp_sessions.go
