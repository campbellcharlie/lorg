package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// Input schema
// ---------------------------------------------------------------------------

type OpenAPIArgs struct {
	Action string `json:"action" jsonschema:"required,enum=import,listEndpoints,generateRequests" jsonschema_description:"import: parse OpenAPI spec; listEndpoints: list discovered routes; generateRequests: build raw HTTP requests from spec"`
	Spec   string `json:"spec,omitempty" jsonschema_description:"OpenAPI 3.x JSON spec string (for import)"`
	Host   string `json:"host,omitempty" jsonschema_description:"Target host for request generation (overrides spec servers)"`
	Path   string `json:"path,omitempty" jsonschema_description:"Filter by specific path prefix"`
	Method string `json:"method,omitempty" jsonschema_description:"Filter by HTTP method"`
}

// ---------------------------------------------------------------------------
// OpenAPI state
// ---------------------------------------------------------------------------

type openAPIEndpoint struct {
	Method      string         `json:"method"`
	Path        string         `json:"path"`
	Summary     string         `json:"summary,omitempty"`
	OperationID string         `json:"operationId,omitempty"`
	Parameters  []openAPIParam `json:"parameters,omitempty"`
	RequestBody *openAPIBody   `json:"requestBody,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
}

type openAPIParam struct {
	Name     string `json:"name"`
	In       string `json:"in"` // query, header, path, cookie
	Required bool   `json:"required"`
	Type     string `json:"type,omitempty"`
	Example  any    `json:"example,omitempty"`
}

type openAPIBody struct {
	ContentType string `json:"contentType"`
	Example     any    `json:"example,omitempty"`
	Schema      any    `json:"schema,omitempty"`
}

var (
	importedEndpoints []openAPIEndpoint
	importedServers   []string
	importedTitle     string
)

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func (backend *Backend) openapiHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args OpenAPIArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "import":
		return backend.openapiImportHandler(args)
	case "listEndpoints":
		return backend.openapiListEndpointsHandler(args)
	case "generateRequests":
		return backend.openapiGenerateRequestsHandler(args)
	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: import, listEndpoints, generateRequests"), nil
	}
}

func (backend *Backend) openapiImportHandler(args OpenAPIArgs) (*mcp.CallToolResult, error) {
	if args.Spec == "" {
		return mcp.NewToolResultError("spec is required for import"), nil
	}

	var spec map[string]any
	if err := json.Unmarshal([]byte(args.Spec), &spec); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid JSON: %v", err)), nil
	}

	// Extract info
	if info, ok := spec["info"].(map[string]any); ok {
		if title, ok := info["title"].(string); ok {
			importedTitle = title
		}
	}

	// Extract servers
	importedServers = nil
	if servers, ok := spec["servers"].([]any); ok {
		for _, s := range servers {
			if sm, ok := s.(map[string]any); ok {
				if url, ok := sm["url"].(string); ok {
					importedServers = append(importedServers, url)
				}
			}
		}
	}

	// Extract paths
	importedEndpoints = nil
	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		return mcp.NewToolResultError("no paths found in spec"), nil
	}

	for path, methods := range paths {
		methodMap, ok := methods.(map[string]any)
		if !ok {
			continue
		}
		for method, details := range methodMap {
			method = strings.ToUpper(method)
			if method == "PARAMETERS" || method == "SUMMARY" || method == "DESCRIPTION" {
				continue // skip path-level metadata
			}

			ep := openAPIEndpoint{
				Method: method,
				Path:   path,
			}

			if detailMap, ok := details.(map[string]any); ok {
				if summary, ok := detailMap["summary"].(string); ok {
					ep.Summary = summary
				}
				if opID, ok := detailMap["operationId"].(string); ok {
					ep.OperationID = opID
				}
				if tags, ok := detailMap["tags"].([]any); ok {
					for _, t := range tags {
						if ts, ok := t.(string); ok {
							ep.Tags = append(ep.Tags, ts)
						}
					}
				}

				// Parse parameters
				if params, ok := detailMap["parameters"].([]any); ok {
					for _, p := range params {
						if pm, ok := p.(map[string]any); ok {
							param := openAPIParam{
								Name:     getStr(pm, "name"),
								In:       getStr(pm, "in"),
								Required: getBool(pm, "required"),
							}
							if schema, ok := pm["schema"].(map[string]any); ok {
								param.Type = getStr(schema, "type")
								param.Example = schema["example"]
							}
							ep.Parameters = append(ep.Parameters, param)
						}
					}
				}

				// Parse requestBody
				if rb, ok := detailMap["requestBody"].(map[string]any); ok {
					if content, ok := rb["content"].(map[string]any); ok {
						for ct, schema := range content {
							body := &openAPIBody{ContentType: ct}
							if sm, ok := schema.(map[string]any); ok {
								body.Schema = sm["schema"]
								if ex, ok := sm["example"]; ok {
									body.Example = ex
								}
							}
							ep.RequestBody = body
							break // take first content type
						}
					}
				}
			}

			importedEndpoints = append(importedEndpoints, ep)
		}
	}

	// Sort by path then method
	sort.Slice(importedEndpoints, func(i, j int) bool {
		if importedEndpoints[i].Path == importedEndpoints[j].Path {
			return importedEndpoints[i].Method < importedEndpoints[j].Method
		}
		return importedEndpoints[i].Path < importedEndpoints[j].Path
	})

	return mcpJSONResult(map[string]any{
		"success":   true,
		"title":     importedTitle,
		"servers":   importedServers,
		"endpoints": len(importedEndpoints),
		"methods":   countMethods(importedEndpoints),
	})
}

func (backend *Backend) openapiListEndpointsHandler(args OpenAPIArgs) (*mcp.CallToolResult, error) {
	if len(importedEndpoints) == 0 {
		return mcp.NewToolResultError("no spec imported. Use action=import first"), nil
	}

	filtered := filterEndpoints(importedEndpoints, args.Path, args.Method)

	return mcpJSONResult(map[string]any{
		"endpoints": filtered,
		"count":     len(filtered),
		"total":     len(importedEndpoints),
	})
}

func (backend *Backend) openapiGenerateRequestsHandler(args OpenAPIArgs) (*mcp.CallToolResult, error) {
	if len(importedEndpoints) == 0 {
		return mcp.NewToolResultError("no spec imported. Use action=import first"), nil
	}

	host := args.Host
	if host == "" && len(importedServers) > 0 {
		host = importedServers[0]
	}
	if host == "" {
		return mcp.NewToolResultError("host is required (no servers in spec)"), nil
	}
	host = strings.TrimRight(host, "/")

	filtered := filterEndpoints(importedEndpoints, args.Path, args.Method)

	type generatedReq struct {
		Method  string `json:"method"`
		Path    string `json:"path"`
		Raw     string `json:"raw"`
		Summary string `json:"summary,omitempty"`
	}

	var requests []generatedReq
	for _, ep := range filtered {
		raw := buildRawFromEndpoint(ep, host)
		requests = append(requests, generatedReq{
			Method:  ep.Method,
			Path:    ep.Path,
			Raw:     raw,
			Summary: ep.Summary,
		})
	}

	return mcpJSONResult(map[string]any{
		"requests": requests,
		"count":    len(requests),
		"host":     host,
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func filterEndpoints(endpoints []openAPIEndpoint, pathPrefix, method string) []openAPIEndpoint {
	filtered := endpoints
	if pathPrefix != "" {
		var f []openAPIEndpoint
		for _, ep := range filtered {
			if strings.HasPrefix(ep.Path, pathPrefix) {
				f = append(f, ep)
			}
		}
		filtered = f
	}
	if method != "" {
		var f []openAPIEndpoint
		for _, ep := range filtered {
			if strings.EqualFold(ep.Method, method) {
				f = append(f, ep)
			}
		}
		filtered = f
	}
	return filtered
}

func buildRawFromEndpoint(ep openAPIEndpoint, host string) string {
	// Build path with query params
	path := ep.Path
	var queryParams []string
	var headerParams []string

	for _, p := range ep.Parameters {
		example := paramExample(p)
		switch p.In {
		case "query":
			queryParams = append(queryParams, fmt.Sprintf("%s=%s", p.Name, example))
		case "header":
			headerParams = append(headerParams, fmt.Sprintf("%s: %s", p.Name, example))
		case "path":
			path = strings.ReplaceAll(path, "{"+p.Name+"}", example)
		}
	}

	if len(queryParams) > 0 {
		path += "?" + strings.Join(queryParams, "&")
	}

	// Determine host header
	hostHeader := host
	hostHeader = strings.TrimPrefix(hostHeader, "https://")
	hostHeader = strings.TrimPrefix(hostHeader, "http://")

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s %s HTTP/1.1\r\n", ep.Method, path))
	b.WriteString(fmt.Sprintf("Host: %s\r\n", hostHeader))

	for _, h := range headerParams {
		b.WriteString(h + "\r\n")
	}

	// Add body for POST/PUT/PATCH
	if ep.RequestBody != nil {
		b.WriteString(fmt.Sprintf("Content-Type: %s\r\n", ep.RequestBody.ContentType))
		var body string
		if ep.RequestBody.Example != nil {
			bodyBytes, _ := json.Marshal(ep.RequestBody.Example)
			body = string(bodyBytes)
		} else if ep.RequestBody.Schema != nil {
			body = generateExampleFromSchema(ep.RequestBody.Schema)
		}
		if body != "" {
			b.WriteString(fmt.Sprintf("Content-Length: %d\r\n", len(body)))
			b.WriteString("\r\n")
			b.WriteString(body)
		} else {
			b.WriteString("\r\n")
		}
	} else {
		b.WriteString("\r\n")
	}

	return b.String()
}

func paramExample(p openAPIParam) string {
	if p.Example != nil {
		return fmt.Sprintf("%v", p.Example)
	}
	switch p.Type {
	case "integer":
		return "1"
	case "boolean":
		return "true"
	case "number":
		return "1.0"
	default:
		return "example"
	}
}

func generateExampleFromSchema(schema any) string {
	schemaMap, ok := schema.(map[string]any)
	if !ok {
		return "{}"
	}

	result := make(map[string]any)
	if props, ok := schemaMap["properties"].(map[string]any); ok {
		for name, propRaw := range props {
			if prop, ok := propRaw.(map[string]any); ok {
				if ex, ok := prop["example"]; ok {
					result[name] = ex
				} else {
					switch getStr(prop, "type") {
					case "string":
						result[name] = "string"
					case "integer":
						result[name] = 0
					case "number":
						result[name] = 0.0
					case "boolean":
						result[name] = false
					case "array":
						result[name] = []any{}
					case "object":
						result[name] = map[string]any{}
					default:
						result[name] = nil
					}
				}
			}
		}
	}

	b, _ := json.MarshalIndent(result, "", "  ")
	return string(b)
}

func getStr(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getBool(m map[string]any, key string) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}

func countMethods(endpoints []openAPIEndpoint) map[string]int {
	counts := make(map[string]int)
	for _, ep := range endpoints {
		counts[ep.Method]++
	}
	return counts
}
