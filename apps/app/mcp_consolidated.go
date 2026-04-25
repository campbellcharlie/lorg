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

// ========================== 11. host ==========================
//
// Folds 8 previous tools (listHosts, getHostInfo, hostPrintSitemap,
// hostPrintRowsInDetails, getNoteForHost, setNoteForHost,
// modifyHostLabels, modifyHostNotes) into one. The nested
// HostLabelAction.Action / HostNoteAction.Action fields don't clash
// with the top-level dispatcher action because they're inside arrays.
type ConsolidatedHostArgs struct {
	Action string `json:"action" jsonschema:"required" jsonschema_description:"Operation: list (paged hosts), info (one host's tech/labels/notes), sitemap (URL tree under path), rows (request rows for a host), getNote (legacy single note), setNote (legacy single note), modifyLabels (add/remove/toggle labels), modifyNotes (add/update/remove notes)"`

	// Common
	Host string `json:"host,omitempty" jsonschema_description:"Host (info, sitemap, rows, getNote, setNote, modifyLabels, modifyNotes). For modify*, include the protocol (e.g. http://example.com)."`

	// list
	Search string `json:"search,omitempty" jsonschema_description:"Search filter for host names (list)"`
	Page   int    `json:"page,omitempty" jsonschema_description:"Page number, 1-indexed (list, rows)"`

	// sitemap
	Path  string `json:"path,omitempty" jsonschema_description:"Path prefix to scope the sitemap (sitemap)"`
	Depth int    `json:"depth,omitempty" jsonschema_description:"Sitemap depth, -1 for full (sitemap)"`

	// rows
	Filter string `json:"filter,omitempty" jsonschema_description:"Substring filter for the host's request rows (rows)"`

	// setNote
	Edit []NoteEditAction `json:"edit,omitempty" jsonschema_description:"Per-line edits for the host's note (setNote)"`

	// modifyLabels / modifyNotes
	Labels []HostLabelAction `json:"labels,omitempty" jsonschema_description:"Label actions to apply (modifyLabels). Each entry has its own action field: add|remove|toggle."`
	Notes  []HostNoteAction  `json:"notes,omitempty" jsonschema_description:"Note actions to apply (modifyNotes). Each entry has its own action field: add|update|remove."`
}

func (backend *Backend) hostHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ConsolidatedHostArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "list":
		return backend.listHostsHandler(ctx, request)
	case "info":
		return backend.getHostInfoHandler(ctx, request)
	case "sitemap":
		return backend.hostPrintSitemapHandler(ctx, request)
	case "rows":
		return backend.hostPrintRowsInDetailsHandler(ctx, request)
	case "getNote":
		return backend.getNoteForHostHandler(ctx, request)
	case "setNote":
		return backend.setNoteForHostHandler(ctx, request)
	case "modifyLabels":
		return backend.modifyHostLabelsHandler(ctx, request)
	case "modifyNotes":
		return backend.modifyHostNotesHandler(ctx, request)
	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: list, info, sitemap, rows, getNote, setNote, modifyLabels, modifyNotes"), nil
	}
}

// ========================== 12. intercept ==========================
//
// One tool that absorbs interceptToggle/interceptPrintRowsInDetails/
// interceptGetRawRequestAndResponse/interceptAction. Action names are
// the leaves of the original tools (toggle/list/getRaw/forward/drop)
// to keep them flat for the LLM. Sub-handler arg structs don't carry
// their own "action" field, so re-binding is safe.
type ConsolidatedInterceptArgs struct {
	Action string `json:"action" jsonschema:"required" jsonschema_description:"Operation: toggle (enable/disable on a proxy), list (intercepted rows for a proxy), getRaw (one record's raw req/resp), forward (pass through, optionally with edits), drop (block)"`

	// toggle: id = proxy ID. forward/drop/getRaw: id = intercept record ID.
	ID     string `json:"id,omitempty" jsonschema_description:"Proxy ID (toggle) or intercept record ID (getRaw, forward, drop)"`
	Enable bool   `json:"enable,omitempty" jsonschema_description:"true to enable, false to disable (toggle)"`

	// list
	ProxyID string `json:"proxyId,omitempty" jsonschema_description:"Proxy ID to list intercepted rows for (list)"`

	// forward/drop edit fields
	IsReqEdited  bool   `json:"isReqEdited,omitempty" jsonschema_description:"True if the request was edited (forward)"`
	IsRespEdited bool   `json:"isRespEdited,omitempty" jsonschema_description:"True if the response was edited (forward)"`
	ReqEdited    string `json:"reqEdited,omitempty" jsonschema_description:"Raw edited HTTP request (forward, if isReqEdited)"`
	RespEdited   string `json:"respEdited,omitempty" jsonschema_description:"Raw edited HTTP response (forward, if isRespEdited)"`
}

func (backend *Backend) interceptHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ConsolidatedInterceptArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "toggle":
		return backend.interceptToggleHandler(ctx, request)
	case "list":
		return backend.interceptReadHandler(ctx, request)
	case "getRaw":
		return backend.interceptGetRawHandler(ctx, request)
	case "forward", "drop":
		// interceptActionHandler re-reads "action" from the raw JSON, which
		// is the same forward/drop value — safe to re-dispatch.
		return backend.interceptActionHandler(ctx, request)
	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: toggle, list, getRaw, forward, drop"), nil
	}
}

// ========================== 12. responseAnalysis ==========================
//
// One tool that absorbs the previous extract/analyze/compare dispatchers
// (which themselves were already 3-way action dispatchers). Action names
// are namespaced (analyze* / extract* / diff*) so the LLM can pick from
// a flat list instead of two-level routing. All sub-handler argument
// field names are compatible across categories — none collide.
type ConsolidatedResponseAnalysisArgs struct {
	Action string `json:"action" jsonschema:"required" jsonschema_description:"Operation: analyzeResponse, analyzeVariations, analyzeKeywords, extractRegex, extractJsonPath, extractBetween, diffResponses, diffById, diffStructural, diffJson"`

	// analyze* inputs
	Response  string   `json:"response,omitempty" jsonschema_description:"Raw HTTP response (analyzeResponse)"`
	Responses []string `json:"responses,omitempty" jsonschema_description:"Multiple raw HTTP responses (analyzeVariations, analyzeKeywords)"`
	Keywords  []string `json:"keywords,omitempty" jsonschema_description:"Keywords to search for (analyzeKeywords)"`

	// extract* inputs
	Content        string `json:"content,omitempty" jsonschema_description:"Content to search in (extract* actions)"`
	Pattern        string `json:"pattern,omitempty" jsonschema_description:"Regex pattern (extractRegex)"`
	MaxMatches     int    `json:"maxMatches,omitempty" jsonschema_description:"Max matches to return (extractRegex, extractBetween)"`
	Group          int    `json:"group,omitempty" jsonschema_description:"Capture group (extractRegex, 0=full match)"`
	Path           string `json:"path,omitempty" jsonschema_description:"Dot-notation JSON path (extractJsonPath)"`
	StartDelimiter string `json:"startDelimiter,omitempty" jsonschema_description:"Start delimiter (extractBetween)"`
	EndDelimiter   string `json:"endDelimiter,omitempty" jsonschema_description:"End delimiter (extractBetween)"`

	// diff* inputs
	Response1     string   `json:"response1,omitempty" jsonschema_description:"First raw HTTP response (diffResponses, diffStructural) or JSON string (diffJson)"`
	Response2     string   `json:"response2,omitempty" jsonschema_description:"Second raw HTTP response (diffResponses, diffStructural) or JSON string (diffJson)"`
	ID1           string   `json:"id1,omitempty" jsonschema_description:"First captured request ID (diffById)"`
	ID2           string   `json:"id2,omitempty" jsonschema_description:"Second captured request ID (diffById)"`
	IgnoreHeaders []string `json:"ignoreHeaders,omitempty" jsonschema_description:"Header names to ignore (diffResponses, diffById, diffStructural)"`
}

func (backend *Backend) responseAnalysisHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ConsolidatedResponseAnalysisArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	// analyze*
	case "analyzeResponse":
		return backend.analyzeResponseHandler(ctx, request)
	case "analyzeVariations":
		return backend.analyzeResponseVariationsHandler(ctx, request)
	case "analyzeKeywords":
		return backend.analyzeResponseKeywordsHandler(ctx, request)

	// extract*
	case "extractRegex":
		return backend.extractRegexHandler(ctx, request)
	case "extractJsonPath":
		return backend.extractJsonPathHandler(ctx, request)
	case "extractBetween":
		return backend.extractBetweenHandler(ctx, request)

	// diff*
	case "diffResponses":
		return backend.compareResponsesHandler(ctx, request)
	case "diffById":
		return backend.compareTrafficByIdHandler(ctx, request)
	case "diffStructural":
		return backend.structuralDiffHandler(ctx, args.Response1, args.Response2, args.IgnoreHeaders)
	case "diffJson":
		return backend.jsonDiffHandler(ctx, args.Response1, args.Response2)

	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: analyzeResponse, analyzeVariations, analyzeKeywords, extractRegex, extractJsonPath, extractBetween, diffResponses, diffById, diffStructural, diffJson"), nil
	}
}
