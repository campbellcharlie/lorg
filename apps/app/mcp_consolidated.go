package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// Consolidated MCP tool dispatchers
//
// Each handler below routes to an existing sub-handler based on an "action"
// parameter. The sub-handlers parse their own args via request.BindArguments,
// so we only need to parse the action field and delegate. Where JSON field
// names conflict between actions, the handler manages the dispatch inline.
// ---------------------------------------------------------------------------

// ========================== 1. encode ==========================

// ConsolidatedEncodeArgs is the union argument struct for the encode tool.
type ConsolidatedEncodeArgs struct {
	Action  string `json:"action" jsonschema:"required" jsonschema_description:"Operation: urlEncode, urlDecode, b64Encode, b64Decode, random"`
	Content string `json:"content,omitempty" jsonschema_description:"Input string (urlEncode, urlDecode, b64Encode, b64Decode)"`
	Length  int    `json:"length,omitempty" jsonschema_description:"Length of random string (random)"`
	Charset string `json:"charset,omitempty" jsonschema_description:"Character set for random generation (random, default: alphanumeric)"`
}

func (backend *Backend) encodeHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ConsolidatedEncodeArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "urlEncode":
		return backend.urlEncodeHandler(ctx, request)
	case "urlDecode":
		return backend.urlDecodeHandler(ctx, request)
	case "b64Encode":
		return backend.base64EncodeHandler(ctx, request)
	case "b64Decode":
		return backend.base64DecodeHandler(ctx, request)
	case "random":
		return backend.generateRandomStringHandler(ctx, request)
	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: urlEncode, urlDecode, b64Encode, b64Decode, random"), nil
	}
}

// ========================== 2. session ==========================

// ConsolidatedSessionArgs is the union argument struct for the session tool.
// Field names are chosen to avoid conflicts between actions that reuse the
// same concept (e.g. "name" means session name for most actions but cookie
// name for setCookie). Actions that would conflict use dedicated fields.
type ConsolidatedSessionArgs struct {
	Action         string            `json:"action" jsonschema:"required" jsonschema_description:"Operation: create, list, switch, delete, getHeaders, updateCookies, getCookies, setCookie, csrfExtract"`
	Name           string            `json:"name,omitempty" jsonschema_description:"Session name (create, switch, delete, getHeaders, updateCookies, getCookies, setCookie)"`
	Cookies        map[string]string `json:"cookies,omitempty" jsonschema_description:"Initial cookies as name:value (create)"`
	Headers        FlexibleStringMap `json:"headers,omitempty" jsonschema_description:"Custom headers (create)"`
	CookieValues   []string          `json:"cookieValues,omitempty" jsonschema_description:"Set-Cookie values to merge (updateCookies)"`
	CookieName     string            `json:"cookieName,omitempty" jsonschema_description:"Cookie name (setCookie)"`
	CookieValue    string            `json:"cookieValue,omitempty" jsonschema_description:"Cookie value (setCookie)"`
	Content        string            `json:"content,omitempty" jsonschema_description:"HTML content to extract CSRF tokens from (csrfExtract)"`
	CustomPatterns []string          `json:"customPatterns,omitempty" jsonschema_description:"Custom regex patterns (csrfExtract)"`
	SessionName    string            `json:"sessionName,omitempty" jsonschema_description:"Session to store extracted CSRF token in (csrfExtract)"`
}

func (backend *Backend) sessionHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ConsolidatedSessionArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "create":
		// Delegate: SessionCreateArgs uses json:"name", json:"cookies" (map[string]string), json:"headers"
		// All field names and types match the consolidated struct.
		return backend.sessionCreateHandler(ctx, request)

	case "list":
		return backend.sessionListHandler(ctx, request)

	case "switch":
		// Delegate: SessionSwitchArgs uses json:"name" -- matches.
		return backend.sessionSwitchHandler(ctx, request)

	case "delete":
		// Delegate: SessionDeleteArgs uses json:"name" -- matches.
		return backend.sessionDeleteHandler(ctx, request)

	case "getHeaders":
		// Delegate: SessionGetHeadersArgs uses json:"name" -- matches.
		return backend.sessionGetHeadersHandler(ctx, request)

	case "updateCookies":
		// Inline: SessionUpdateCookiesArgs uses json:"cookies" ([]string), but
		// our consolidated struct uses json:"cookieValues" for the []string
		// and json:"cookies" for map[string]string (create). Must remap.
		record, err := backend.resolveSession(args.Name)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		existing, _ := record.Get("cookies").(map[string]any)
		if existing == nil {
			existing = make(map[string]any)
		}

		for _, raw := range args.CookieValues {
			cookiePart := raw
			if idx := strings.Index(raw, ";"); idx != -1 {
				cookiePart = raw[:idx]
			}
			cookiePart = strings.TrimSpace(cookiePart)

			eqIdx := strings.Index(cookiePart, "=")
			if eqIdx == -1 {
				continue
			}
			name := strings.TrimSpace(cookiePart[:eqIdx])
			value := strings.TrimSpace(cookiePart[eqIdx+1:])
			existing[name] = value
		}

		record.Set("cookies", existing)
		if err := backend.DB.SaveRecord(record); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to update cookies: %v", err)), nil
		}

		return mcpJSONResult(map[string]any{
			"success": true,
			"session": record.GetString("name"),
			"cookies": existing,
		})

	case "getCookies":
		// Inline: GetCookieJarArgs uses json:"session", but our consolidated
		// struct uses json:"name" for the session name. Must remap.
		session, err := backend.resolveSession(args.Name)
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

	case "setCookie":
		// Inline: SetCookieArgs uses json:"name" for cookie name and
		// json:"session" for session name. Our consolidated struct uses
		// json:"name" for session name and json:"cookieName"/"cookieValue"
		// for the cookie. Must remap.
		if args.CookieName == "" {
			return mcp.NewToolResultError("cookieName is required for setCookie"), nil
		}

		session, err := backend.resolveSession(args.Name)
		if err != nil {
			return mcp.NewToolResultError("no session found: create one with sessionCreate and activate with sessionSwitch"), nil
		}

		cookies := session.Get("cookies")
		cookieMap, ok := cookies.(map[string]any)
		if !ok {
			cookieMap = make(map[string]any)
		}

		cookieMap[args.CookieName] = args.CookieValue
		session.Set("cookies", cookieMap)

		if err := backend.DB.SaveRecord(session); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to save cookie: %v", err)), nil
		}

		return mcpJSONResult(map[string]any{
			"success":      true,
			"session":      session.GetString("name"),
			"cookie":       map[string]string{"name": args.CookieName, "value": args.CookieValue},
			"totalCookies": len(cookieMap),
		})

	case "csrfExtract":
		// Delegate: CsrfExtractArgs uses json:"content", json:"customPatterns",
		// json:"sessionName" -- all match consolidated struct field names.
		return backend.csrfExtractHandler(ctx, request)

	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: create, list, switch, delete, getHeaders, updateCookies, getCookies, setCookie, csrfExtract"), nil
	}
}

// ========================== 3. scope ==========================

// ConsolidatedScopeArgs is the union argument struct for the scope tool.
type ConsolidatedScopeArgs struct {
	Action   string `json:"action" jsonschema:"required" jsonschema_description:"Operation: load, check, checkMultiple, getRules, addRule, removeRule, reset"`
	FilePath string `json:"filePath,omitempty" jsonschema_description:"Path to scope.yaml (load)"`
	Url      string `json:"url,omitempty" jsonschema_description:"URL to check (check)"`
	Urls     []string `json:"urls,omitempty" jsonschema_description:"URLs to check (checkMultiple)"`
	RuleType string `json:"ruleType,omitempty" jsonschema_description:"include or exclude (addRule, removeRule)"`
	Host     string `json:"host,omitempty" jsonschema_description:"Host pattern (addRule)"`
	Protocol string `json:"protocol,omitempty" jsonschema_description:"http, https, or empty (addRule)"`
	Port     string `json:"port,omitempty" jsonschema_description:"Port or empty (addRule)"`
	Path     string `json:"path,omitempty" jsonschema_description:"Path prefix or empty (addRule)"`
	Reason   string `json:"reason,omitempty" jsonschema_description:"Reason for rule (addRule)"`
	Index    int    `json:"index,omitempty" jsonschema_description:"Rule index to remove (removeRule)"`
	Confirm  bool   `json:"confirm,omitempty" jsonschema_description:"Must be true to confirm (reset)"`
}

func (backend *Backend) scopeHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ConsolidatedScopeArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "load":
		// Delegate: ScopeLoadArgs uses json:"filePath" -- matches.
		return backend.scopeLoadHandler(ctx, request)

	case "check":
		// Delegate: ScopeCheckArgs uses json:"url" -- matches.
		return backend.scopeCheckHandler(ctx, request)

	case "checkMultiple":
		// Delegate: ScopeCheckMultipleArgs uses json:"urls" -- matches.
		return backend.scopeCheckMultipleHandler(ctx, request)

	case "getRules":
		return backend.scopeGetRulesHandler(ctx, request)

	case "addRule":
		// Inline: ScopeAddRuleArgs uses json:"type" for include/exclude, but our
		// consolidated struct uses json:"ruleType" to avoid ambiguity. Must remap.
		if args.RuleType != "include" && args.RuleType != "exclude" {
			return mcp.NewToolResultError("ruleType must be 'include' or 'exclude'"), nil
		}

		rule := ScopeRule{
			Protocol: args.Protocol,
			Host:     args.Host,
			Port:     args.Port,
			Path:     args.Path,
			Reason:   args.Reason,
		}

		scopeManager.AddRule(args.RuleType, rule)

		return mcpJSONResult(map[string]any{
			"success": true,
			"type":    args.RuleType,
			"rule":    rule,
		})

	case "removeRule":
		// Inline: ScopeRemoveRuleArgs uses json:"type" and json:"index", but our
		// consolidated struct uses json:"ruleType". Must remap.
		if args.RuleType != "include" && args.RuleType != "exclude" {
			return mcp.NewToolResultError("ruleType must be 'include' or 'exclude'"), nil
		}

		if err := scopeManager.RemoveRule(args.RuleType, args.Index); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcpJSONResult(map[string]any{
			"success":      true,
			"type":         args.RuleType,
			"removedIndex": args.Index,
		})

	case "reset":
		// Delegate: ScopeResetArgs uses json:"confirm" -- matches.
		return backend.scopeResetHandler(ctx, request)

	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: load, check, checkMultiple, getRules, addRule, removeRule, reset"), nil
	}
}

// ========================== 4. jwt ==========================

// ConsolidatedJwtArgs is the union argument struct for the jwt tool.
type ConsolidatedJwtArgs struct {
	Action     string   `json:"action" jsonschema:"required" jsonschema_description:"Operation: decode, forge, noneAttack, keyConfusion, bruteforce"`
	Token      string   `json:"token,omitempty" jsonschema_description:"JWT token (decode, noneAttack, keyConfusion, bruteforce)"`
	Header     string   `json:"header,omitempty" jsonschema_description:"JWT header JSON (forge)"`
	Payload    string   `json:"payload,omitempty" jsonschema_description:"JWT payload JSON (forge)"`
	Secret     string   `json:"secret,omitempty" jsonschema_description:"HMAC secret for HS256/384/512 (forge)"`
	PrivateKey string   `json:"privateKey,omitempty" jsonschema_description:"RSA private key PEM (forge)"`
	PublicKey  string   `json:"publicKey,omitempty" jsonschema_description:"RSA public key PEM (keyConfusion)"`
	Wordlist   []string `json:"wordlist,omitempty" jsonschema_description:"Custom secrets to try (bruteforce)"`
}

func (backend *Backend) jwtHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ConsolidatedJwtArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "decode":
		return backend.jwtDecodeHandler(ctx, request)
	case "forge":
		return backend.jwtForgeHandler(ctx, request)
	case "noneAttack":
		return backend.jwtNoneAttackHandler(ctx, request)
	case "keyConfusion":
		return backend.jwtKeyConfusionHandler(ctx, request)
	case "bruteforce":
		return backend.jwtBruteforceHandler(ctx, request)
	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: decode, forge, noneAttack, keyConfusion, bruteforce"), nil
	}
}

// ========================== 5. template ==========================

// ConsolidatedTemplateArgs is the union argument struct for the template tool.
type ConsolidatedTemplateArgs struct {
	Action          string                `json:"action" jsonschema:"required" jsonschema_description:"Operation: register, send, sendBatch, sendSequence, list, delete"`
	Name            string                `json:"name,omitempty" jsonschema_description:"Template name (register, delete)"`
	TLS             bool                  `json:"tls,omitempty" jsonschema_description:"Use HTTPS (register)"`
	Host            string                `json:"host,omitempty" jsonschema_description:"Target hostname (register)"`
	Port            int                   `json:"port,omitempty" jsonschema_description:"Target port (register)"`
	HttpVersion     int                   `json:"httpVersion,omitempty" jsonschema_description:"HTTP version: 1 or 2 (register)"`
	RequestTemplate string                `json:"requestTemplate,omitempty" jsonschema_description:"Raw HTTP request with ${VAR} placeholders (register)"`
	Variables       map[string]string     `json:"variables,omitempty" jsonschema_description:"Default variable values (register) or overrides (send)"`
	Description     string                `json:"description,omitempty" jsonschema_description:"Template description (register)"`
	InjectSession   bool                  `json:"injectSession,omitempty" jsonschema_description:"Auto-inject active session cookies/headers (register)"`
	JsonEscapeVars  bool                  `json:"jsonEscapeVars,omitempty" jsonschema_description:"JSON-escape variable values before substitution (register)"`
	ExtractRegex    string                `json:"extractRegex,omitempty" jsonschema_description:"Regex to extract from response (register)"`
	ExtractGroup    int                   `json:"extractGroup,omitempty" jsonschema_description:"Capture group for extraction (register)"`
	TemplateName    string                `json:"templateName,omitempty" jsonschema_description:"Template name to use (send, sendBatch)"`
	VariableSets    []map[string]string   `json:"variableSets,omitempty" jsonschema_description:"Array of variable sets (sendBatch)"`
	Steps           []TemplateSequenceStep `json:"steps,omitempty" jsonschema_description:"Ordered steps to execute (sendSequence)"`
	Note            string                `json:"note,omitempty" jsonschema_description:"Note to attach to request (send, sendBatch)"`
}

func (backend *Backend) templateHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ConsolidatedTemplateArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "register":
		// Delegate: all field names match TemplateRegisterArgs.
		return backend.templateRegisterHandler(ctx, request)
	case "send":
		// Delegate: TemplateSendFromArgs uses json:"templateName", json:"variables", json:"note" -- all match.
		return backend.templateSendFromHandler(ctx, request)
	case "sendBatch":
		// Delegate: TemplateSendBatchArgs uses json:"templateName", json:"variableSets", json:"note" -- all match.
		return backend.templateSendBatchHandler(ctx, request)
	case "sendSequence":
		// Delegate: TemplateSendSequenceArgs uses json:"steps" -- matches.
		return backend.templateSendSequenceHandler(ctx, request)
	case "list":
		return backend.templateListHandler(ctx, request)
	case "delete":
		// Delegate: TemplateDeleteArgs uses json:"name" -- matches.
		return backend.templateDeleteHandler(ctx, request)
	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: register, send, sendBatch, sendSequence, list, delete"), nil
	}
}

// ========================== 6. trafficTag ==========================

// ConsolidatedTrafficTagArgs is the union argument struct for the trafficTag tool.
type ConsolidatedTrafficTagArgs struct {
	Action    string `json:"action" jsonschema:"required" jsonschema_description:"Operation: add, get, list, delete"`
	RequestID int    `json:"requestId,omitempty" jsonschema_description:"request_id from project DB (add, get, delete)"`
	Tag       string `json:"tag,omitempty" jsonschema_description:"Tag name (add, get, delete)"`
	Note      string `json:"note,omitempty" jsonschema_description:"Optional note (add)"`
	Limit     int    `json:"limit,omitempty" jsonschema_description:"Max results (get, default: 100)"`
}

func (backend *Backend) trafficTagHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ConsolidatedTrafficTagArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "add":
		// Delegate: TagTrafficArgs uses json:"requestId", json:"tag", json:"note" -- all match.
		return backend.tagTrafficHandler(ctx, request)
	case "get":
		// Delegate: GetTaggedTrafficArgs uses json:"tag", json:"limit" -- all match.
		return backend.getTaggedTrafficHandler(ctx, request)
	case "list":
		return backend.listTagsHandler(ctx, request)
	case "delete":
		// Delegate: DeleteTrafficTagArgs uses json:"requestId", json:"tag" -- all match.
		return backend.deleteTrafficTagHandler(ctx, request)
	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: add, get, list, delete"), nil
	}
}

// ========================== 7. project ==========================

// ConsolidatedProjectArgs is the union argument struct for the project tool.
type ConsolidatedProjectArgs struct {
	Action        string   `json:"action" jsonschema:"required" jsonschema_description:"Operation: setup, info, setName, export, setLogging, setRedactionMode, getRedactionMode"`
	Name          string   `json:"name,omitempty" jsonschema_description:"Project name (setup, setName)"`
	DbDir         string   `json:"dbDir,omitempty" jsonschema_description:"Directory for SQLite DB files (setup)"`
	OutputPath    string   `json:"outputPath,omitempty" jsonschema_description:"Output path for export (export)"`
	ProjectName   string   `json:"projectName,omitempty" jsonschema_description:"Project name for export metadata (export)"`
	HostFilter    string   `json:"hostFilter,omitempty" jsonschema_description:"Only export traffic for this host (export)"`
	Enabled       bool     `json:"enabled,omitempty" jsonschema_description:"Enable or disable traffic logging (setLogging)"`
	Sources       []string `json:"sources,omitempty" jsonschema_description:"Which sources to log: proxy, repeater, mcp, template, all (setLogging)"`
	RedactionMode string   `json:"redactionMode,omitempty" jsonschema_description:"Redaction mode: off, balanced, strict (setRedactionMode)"`
}

func (backend *Backend) projectHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ConsolidatedProjectArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "setup":
		// Delegate: ProjectSetupArgs uses json:"name", json:"dbDir" -- all match.
		return backend.projectSetupHandler(ctx, request)
	case "info":
		return backend.projectInfoHandler(ctx, request)
	case "setName":
		// Delegate: ProjectSetNameArgs uses json:"name" -- matches.
		return backend.projectSetNameHandler(ctx, request)
	case "export":
		// Delegate: ProjectExportArgs uses json:"outputPath", json:"projectName", json:"hostFilter" -- all match.
		return backend.projectExportHandler(ctx, request)
	case "setLogging":
		// Delegate: SetTrafficLoggingArgs uses json:"enabled", json:"sources" -- all match.
		return backend.setTrafficLoggingHandler(ctx, request)
	case "setRedactionMode":
		return backend.setRedactionModeHandler(ctx, request)
	case "getRedactionMode":
		return backend.getRedactionModeHandler(ctx, request)
	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: setup, info, setName, export, setLogging, setRedactionMode, getRedactionMode"), nil
	}
}

// ========================== 8. raceTest ==========================

// ConsolidatedRaceTestArgs is the union argument struct for the raceTest tool.
type ConsolidatedRaceTestArgs struct {
	Action   string   `json:"action" jsonschema:"required" jsonschema_description:"Operation: parallel, parallelDifferent, h2SinglePacket, lastByteSync, firstSequenceSync"`
	Host     string   `json:"host,omitempty" jsonschema_description:"Target hostname"`
	Port     int      `json:"port,omitempty" jsonschema_description:"Target port"`
	TLS      bool     `json:"tls,omitempty" jsonschema_description:"Use TLS"`
	Request  string   `json:"request,omitempty" jsonschema_description:"Raw HTTP request (parallel, lastByteSync)"`
	Requests []string `json:"requests,omitempty" jsonschema_description:"Different raw HTTP requests (parallelDifferent, h2SinglePacket)"`
	Count    int      `json:"count,omitempty" jsonschema_description:"Number of identical requests (parallel, lastByteSync, max 50)"`
	Note     string   `json:"note,omitempty" jsonschema_description:"Note to attach"`
}

func (backend *Backend) raceTestHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ConsolidatedRaceTestArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "parallel":
		// Delegate: SendParallelArgs uses json:"host", json:"port", json:"tls",
		// json:"request", json:"count", json:"note" -- all match.
		return backend.sendParallelHandler(ctx, request)
	case "parallelDifferent":
		// Delegate: SendParallelDifferentArgs uses json:"host", json:"port", json:"tls",
		// json:"requests", json:"note" -- all match.
		return backend.sendParallelDifferentHandler(ctx, request)
	case "h2SinglePacket":
		// Delegate: SendParallelH2Args uses json:"host", json:"port",
		// json:"requests", json:"note" -- all match.
		return backend.sendParallelH2Handler(ctx, request)
	case "lastByteSync":
		// Delegate: LastByteSyncArgs uses json:"host", json:"port", json:"tls",
		// json:"request", json:"count", json:"note" -- all match.
		return backend.lastByteSyncHandler(ctx, request)
	case "firstSequenceSync":
		// Opens N connections in parallel, waits for all handshakes, then sends
		// the full request simultaneously on all connections.
		return backend.firstSequenceSyncHandler(ctx, request)
	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: parallel, parallelDifferent, h2SinglePacket, lastByteSync, firstSequenceSync"), nil
	}
}

// ========================== 9. sendRaw ==========================

// ConsolidatedSendRawArgs is the union argument struct for the sendRaw tool.
type ConsolidatedSendRawArgs struct {
	Action             string              `json:"action" jsonschema:"required" jsonschema_description:"Operation: tcp, tls, h2Sequence"`
	Host               string              `json:"host,omitempty" jsonschema_description:"Target hostname"`
	Port               int                 `json:"port,omitempty" jsonschema_description:"Target port"`
	Segments           []RawSegment        `json:"segments,omitempty" jsonschema_description:"Data segments to send in order (tcp, tls)"`
	ConnectTimeoutMs   int                 `json:"connectTimeoutMs,omitempty" jsonschema_description:"Connection timeout in ms"`
	ReadTimeoutMs      int                 `json:"readTimeoutMs,omitempty" jsonschema_description:"Read timeout in ms"`
	MaxReadBytes       int                 `json:"maxReadBytes,omitempty" jsonschema_description:"Maximum bytes to read from response"`
	PreviewBytes       int                 `json:"previewBytes,omitempty" jsonschema_description:"Bytes to include as UTF-8 preview (default: 500)"`
	AlpnProtocols      []string            `json:"alpnProtocols,omitempty" jsonschema_description:"ALPN protocols (tls)"`
	InsecureSkipVerify bool                `json:"insecureSkipVerify,omitempty" jsonschema_description:"Skip TLS certificate verification (tls, h2Sequence)"`
	Requests           []H2SequenceRequest `json:"requests,omitempty" jsonschema_description:"HTTP requests to send sequentially (h2Sequence)"`
}

func (backend *Backend) sendRawHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ConsolidatedSendRawArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "tcp":
		// Delegate: SendRawTcpArgs field names all match.
		return backend.sendRawTcpHandler(ctx, request)
	case "tls":
		// Delegate: SendRawTlsArgs field names all match.
		return backend.sendRawTlsHandler(ctx, request)
	case "h2Sequence":
		// Delegate: SendHttp2SequenceArgs field names all match.
		return backend.sendHttp2SequenceHandler(ctx, request)
	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: tcp, tls, h2Sequence"), nil
	}
}

// ========================== 10. graphql ==========================

// ConsolidatedGraphqlArgs is the union argument struct for the graphql tool.
type ConsolidatedGraphqlArgs struct {
	Action          string            `json:"action" jsonschema:"required" jsonschema_description:"Operation: introspect, buildQuery, suggestPayloads"`
	Host            string            `json:"host,omitempty" jsonschema_description:"Target hostname (introspect)"`
	Port            int               `json:"port,omitempty" jsonschema_description:"Target port (introspect)"`
	TLS             bool              `json:"tls,omitempty" jsonschema_description:"Use HTTPS (introspect)"`
	Path            string            `json:"path,omitempty" jsonschema_description:"GraphQL endpoint path (introspect, default: /graphql)"`
	Headers         map[string]string `json:"headers,omitempty" jsonschema_description:"Additional headers (introspect)"`
	BypassTechnique string            `json:"bypassTechnique,omitempty" jsonschema_description:"Bypass: none, get, newline, whitespace, aliased, fragments (introspect)"`
	Schema          string            `json:"schema,omitempty" jsonschema_description:"Schema JSON from introspect (buildQuery)"`
	TypeName        string            `json:"typeName,omitempty" jsonschema_description:"Type name to build query for (buildQuery)"`
	MaxDepth        int               `json:"maxDepth,omitempty" jsonschema_description:"Maximum nesting depth (buildQuery, default: 2)"`
	Category        string            `json:"category,omitempty" jsonschema_description:"Attack category: injection, auth_bypass, info_disclosure, dos, batching, all (suggestPayloads)"`
}

func (backend *Backend) graphqlHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ConsolidatedGraphqlArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "introspect":
		// Delegate: GraphqlIntrospectArgs field names all match.
		return backend.graphqlIntrospectHandler(ctx, request)
	case "buildQuery":
		// Delegate: GraphqlBuildQueryArgs field names all match.
		return backend.graphqlBuildQueryHandler(ctx, request)
	case "suggestPayloads":
		// Delegate: GraphqlSuggestPayloadsArgs uses json:"category" -- matches.
		return backend.graphqlSuggestPayloadsHandler(ctx, request)
	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: introspect, buildQuery, suggestPayloads"), nil
	}
}

// ========================== 11. extract ==========================

// ConsolidatedExtractArgs is the union argument struct for the extract tool.
type ConsolidatedExtractArgs struct {
	Action         string `json:"action" jsonschema:"required" jsonschema_description:"Operation: regex, jsonPath, between"`
	Content        string `json:"content,omitempty" jsonschema_description:"Content to search in"`
	Pattern        string `json:"pattern,omitempty" jsonschema_description:"Regex pattern (regex)"`
	MaxMatches     int    `json:"maxMatches,omitempty" jsonschema_description:"Max matches to return (regex, between)"`
	Group          int    `json:"group,omitempty" jsonschema_description:"Capture group to extract (regex, 0=full match)"`
	Path           string `json:"path,omitempty" jsonschema_description:"Dot-notation JSON path (jsonPath)"`
	StartDelimiter string `json:"startDelimiter,omitempty" jsonschema_description:"Start delimiter (between)"`
	EndDelimiter   string `json:"endDelimiter,omitempty" jsonschema_description:"End delimiter (between)"`
}

func (backend *Backend) extractHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ConsolidatedExtractArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "regex":
		// Delegate: ExtractRegexArgs uses json:"content", json:"pattern",
		// json:"maxMatches", json:"group" -- all match.
		return backend.extractRegexHandler(ctx, request)
	case "jsonPath":
		// Delegate: ExtractJsonPathArgs uses json:"content", json:"path" -- all match.
		return backend.extractJsonPathHandler(ctx, request)
	case "between":
		// Delegate: ExtractBetweenArgs uses json:"content", json:"startDelimiter",
		// json:"endDelimiter", json:"maxMatches" -- all match.
		return backend.extractBetweenHandler(ctx, request)
	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: regex, jsonPath, between"), nil
	}
}

// ========================== 12. analyze ==========================

// ConsolidatedAnalyzeArgs is the union argument struct for the analyze tool.
type ConsolidatedAnalyzeArgs struct {
	Action    string   `json:"action" jsonschema:"required" jsonschema_description:"Operation: response, variations, keywords"`
	Response  string   `json:"response,omitempty" jsonschema_description:"Raw HTTP response (response)"`
	Responses []string `json:"responses,omitempty" jsonschema_description:"Multiple raw HTTP responses (variations, keywords)"`
	Keywords  []string `json:"keywords,omitempty" jsonschema_description:"Keywords to search for (keywords)"`
}

func (backend *Backend) analyzeHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ConsolidatedAnalyzeArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "response":
		// Delegate: AnalyzeResponseArgs uses json:"response" -- matches.
		return backend.analyzeResponseHandler(ctx, request)
	case "variations":
		// Delegate: AnalyzeResponseVariationsArgs uses json:"responses" -- matches.
		return backend.analyzeResponseVariationsHandler(ctx, request)
	case "keywords":
		// Delegate: AnalyzeResponseKeywordsArgs uses json:"responses", json:"keywords" -- all match.
		return backend.analyzeResponseKeywordsHandler(ctx, request)
	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: response, variations, keywords"), nil
	}
}

// ========================== 13. compare ==========================

// ConsolidatedCompareArgs is the union argument struct for the compare tool.
type ConsolidatedCompareArgs struct {
	Action        string   `json:"action" jsonschema:"required" jsonschema_description:"Operation: responses, byId, structural, jsonDiff"`
	Response1     string   `json:"response1,omitempty" jsonschema_description:"First raw HTTP response (responses, structural) or JSON string (jsonDiff)"`
	Response2     string   `json:"response2,omitempty" jsonschema_description:"Second raw HTTP response (responses, structural) or JSON string (jsonDiff)"`
	ID1           string   `json:"id1,omitempty" jsonschema_description:"First request ID from PocketBase (byId)"`
	ID2           string   `json:"id2,omitempty" jsonschema_description:"Second request ID from PocketBase (byId)"`
	IgnoreHeaders []string `json:"ignoreHeaders,omitempty" jsonschema_description:"Header names to ignore in comparison (responses, byId, structural)"`
}

func (backend *Backend) compareHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ConsolidatedCompareArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "responses":
		// Delegate: CompareResponsesArgs uses json:"response1", json:"response2",
		// json:"ignoreHeaders" -- all match.
		return backend.compareResponsesHandler(ctx, request)
	case "byId":
		// Delegate: CompareTrafficByIdArgs uses json:"id1", json:"id2",
		// json:"ignoreHeaders" -- all match.
		return backend.compareTrafficByIdHandler(ctx, request)
	case "structural":
		// Header-by-header + body structure diff
		return backend.structuralDiffHandler(ctx, args.Response1, args.Response2, args.IgnoreHeaders)
	case "jsonDiff":
		// Standalone JSON tree diff (response1/response2 hold JSON strings)
		return backend.jsonDiffHandler(ctx, args.Response1, args.Response2)
	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: responses, byId, structural, jsonDiff"), nil
	}
}
