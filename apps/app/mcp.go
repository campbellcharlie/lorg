package app

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/campbellcharlie/lorg/lrx/version"
	"github.com/campbellcharlie/lorg/internal/save"
	"github.com/labstack/echo/v4"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// ---------------------------------------------------------------------------
// MCP state
// ---------------------------------------------------------------------------

type MCP struct {
	server    *mcpserver.MCPServer
	sseServer *mcpserver.SSEServer
	active    bool
	conns     atomic.Int64
}

// ---------------------------------------------------------------------------
// Tool registration
// ---------------------------------------------------------------------------

func (backend *Backend) mcpInit() {
	s := mcpserver.NewMCPServer(
		"lorg",
		version.CURRENT_BACKEND_VERSION,
		mcpserver.WithToolCapabilities(true),
	)

	// =====================================================================
	// UTILITY
	// =====================================================================

	s.AddTool(
		mcp.NewTool("lorgStatus",
			mcp.WithDescription("Check if the lorg is active"),
		),
		backend.versionHandler,
	)

	s.AddTool(
		mcp.NewTool("encode",
			mcp.WithDescription("Encode/decode strings or generate random values. Actions: urlEncode, urlDecode, b64Encode, b64Decode, random"),
			mcp.WithInputSchema[ConsolidatedEncodeArgs](),
		),
		backend.encodeHandler,
	)

	// =====================================================================
	// DATA & HOST TOOLS (kept individual — core proxy functionality)
	// =====================================================================

	s.AddTool(
		mcp.NewTool("getRequestResponseFromID",
			mcp.WithDescription("Get the active request and response for active ID"),
			mcp.WithInputSchema[GetRequestResponseArgs](),
		),
		backend.getRequestResponseFromIDHandler,
	)

	s.AddTool(
		mcp.NewTool("host",
			mcp.WithDescription("Host inventory + per-host details in one tool. Actions: list (paged hosts with tech/labels), info (one host), sitemap (URL tree under path), rows (request rows for a host), getNote/setNote (legacy single note), modifyLabels (add/remove/toggle), modifyNotes (add/update/remove). Replaces listHosts/getHostInfo/hostPrintSitemap/hostPrintRowsInDetails/getNoteForHost/setNoteForHost/modifyHostLabels/modifyHostNotes."),
			mcp.WithInputSchema[ConsolidatedHostArgs](),
		),
		backend.hostHandler,
	)

	// =====================================================================
	// REQUEST SENDING
	// =====================================================================

	s.AddTool(
		mcp.NewTool("sendHttpRequest",
			mcp.WithDescription("Send a structured HTTP request: method, url, headers, body, with session injection, CSRF handling, redirect following, and optional regex extraction from the response. The primary request tool — use this for one-off requests where you have all the parameters. For re-firing a captured request with small mutations, use mirror() instead (much cheaper). For raw byte control over the request line, use sendRaw."),
			mcp.WithInputSchema[SendHttpRequestArgs](),
		),
		backend.sendHttpRequestHandler,
	)

	s.AddTool(
		mcp.NewTool("exportCurl",
			mcp.WithDescription("Export a stored request as a curl command for use in reports or manual testing"),
			mcp.WithInputSchema[ExportCurlArgs](),
		),
		backend.exportCurlHandler,
	)

	// --- Intercept tools ---

	s.AddTool(
		mcp.NewTool("intercept",
			mcp.WithDescription("Manage proxy interception in one tool. Actions: toggle (enable/disable on a proxy), list (intercepted rows for a proxy), getRaw (raw req/resp for one record), forward (pass through, optional edits), drop (block). Replaces interceptToggle/interceptPrintRowsInDetails/interceptGetRawRequestAndResponse/interceptAction."),
			mcp.WithInputSchema[ConsolidatedInterceptArgs](),
		),
		backend.interceptHandler,
	)

	// --- Match & Replace ---
	s.AddTool(
		mcp.NewTool("matchReplace",
			mcp.WithDescription("Manage proxy match & replace rules. Auto-modify HTTP requests/responses as they pass through the proxy. Actions: add, list, remove, enable, disable, reload"),
			mcp.WithInputSchema[MatchReplaceArgs](),
		),
		backend.matchReplaceHandler,
	)

	// --- Proxy tools ---

	s.AddTool(
		mcp.NewTool("proxyList",
			mcp.WithDescription("Get a list of all running proxy instances with their status, browser type, and configuration"),
		),
		backend.proxyListHandler,
	)

	s.AddTool(
		mcp.NewTool("proxyStart",
			mcp.WithDescription("Start a new proxy instance with optional browser attachment (chrome, firefox, or none)"),
			mcp.WithInputSchema[ProxyStartArgs](),
		),
		backend.proxyStartHandler,
	)

	s.AddTool(
		mcp.NewTool("proxyStop",
			mcp.WithDescription("Stop a running proxy instance by ID, or stop all proxies if no ID is provided"),
			mcp.WithInputSchema[ProxyStopArgs](),
		),
		backend.proxyStopHandler,
	)

	// Chrome/CDP page tools removed in favor of CamoFox (`browser*` tools).
	// The REST routes for these still exist for the frontend UI; only the
	// MCP exposure is dropped. Use proxyStart with browser:firefox or none.

	// =====================================================================
	// CONSOLIDATED TOOLS (action-dispatched)
	// =====================================================================

	// encode is registered above in Utility section

	s.AddTool(
		mcp.NewTool("session",
			mcp.WithDescription("Manage sessions, cookies, and CSRF tokens. Actions: create, list, switch, delete, getHeaders, updateCookies, getCookies, setCookie, csrfExtract"),
			mcp.WithInputSchema[ConsolidatedSessionArgs](),
		),
		backend.sessionHandler,
	)

	s.AddTool(
		mcp.NewTool("scope",
			mcp.WithDescription("Manage scope rules for target URL filtering. Actions: load, check, checkMultiple, getRules, addRule, removeRule, reset"),
			mcp.WithInputSchema[ConsolidatedScopeArgs](),
		),
		backend.scopeHandler,
	)

	s.AddTool(
		mcp.NewTool("jwt",
			mcp.WithDescription("JWT security testing. Actions: decode, forge, noneAttack, keyConfusion, bruteforce"),
			mcp.WithInputSchema[ConsolidatedJwtArgs](),
		),
		backend.jwtHandler,
	)

	s.AddTool(
		mcp.NewTool("template",
			mcp.WithDescription("Manage and execute request templates with variable substitution. Actions: register, send, sendBatch, sendSequence, list, delete"),
			mcp.WithInputSchema[ConsolidatedTemplateArgs](),
		),
		backend.templateHandler,
	)

	s.AddTool(
		mcp.NewTool("trafficTag",
			mcp.WithDescription("Tag and annotate traffic in the project DB. Actions: add, get, list, delete"),
			mcp.WithInputSchema[ConsolidatedTrafficTagArgs](),
		),
		backend.trafficTagHandler,
	)

	s.AddTool(
		mcp.NewTool("project",
			mcp.WithDescription("Manage project settings, SQLite DB, traffic logging, and privacy. Actions: setup, info, setName, export, setLogging, setRedactionMode, getRedactionMode"),
			mcp.WithInputSchema[ConsolidatedProjectArgs](),
		),
		backend.projectHandler,
	)

	s.AddTool(
		mcp.NewTool("raceTest",
			mcp.WithDescription("Race condition testing with simultaneous requests. Actions: parallel, parallelDifferent, h2SinglePacket, lastByteSync, firstSequenceSync"),
			mcp.WithInputSchema[ConsolidatedRaceTestArgs](),
		),
		backend.raceTestHandler,
	)

	s.AddTool(
		mcp.NewTool("sendRaw",
			mcp.WithDescription("Send raw TCP/TLS bytes or HTTP/2 sequences. Actions: tcp, tls, h2Sequence"),
			mcp.WithInputSchema[ConsolidatedSendRawArgs](),
		),
		backend.sendRawHandler,
	)

	s.AddTool(
		mcp.NewTool("graphql",
			mcp.WithDescription("GraphQL security testing. Actions: introspect (with bypass techniques), buildQuery, suggestPayloads"),
			mcp.WithInputSchema[ConsolidatedGraphqlArgs](),
		),
		backend.graphqlHandler,
	)

	s.AddTool(
		mcp.NewTool("openapi",
			mcp.WithDescription("OpenAPI/Swagger spec import and request generation. Actions: import (parse JSON spec), listEndpoints (show routes), generateRequests (build raw HTTP requests)"),
			mcp.WithInputSchema[OpenAPIArgs](),
		),
		backend.openapiHandler,
	)

	s.AddTool(
		mcp.NewTool("ja4",
			mcp.WithDescription("JA4+ TLS fingerprint lookup from proxy traffic. Actions: lookup (by host), list (all cached)"),
			mcp.WithInputSchema[JA4Args](),
		),
		backend.ja4Handler,
	)

	s.AddTool(
		mcp.NewTool("responseAnalysis",
			mcp.WithDescription("Inspect, extract from, or diff HTTP responses. Actions: analyzeResponse|analyzeVariations|analyzeKeywords (parse headers/cookies/tech, find invariants, track keywords across many responses), extractRegex|extractJsonPath|extractBetween (pull values out of content), diffResponses|diffById|diffStructural|diffJson (compare two responses by raw text, captured row id, structure, or JSON tree)."),
			mcp.WithInputSchema[ConsolidatedResponseAnalysisArgs](),
		),
		backend.responseAnalysisHandler,
	)

	s.AddTool(
		mcp.NewTool("oob",
			mcp.WithDescription("Out-of-band callback server for blind vulnerability detection. Actions: start, stop, generatePayload, pollInteractions, clearInteractions"),
			mcp.WithInputSchema[OOBArgs](),
		),
		backend.oobHandler,
	)

	s.AddTool(
		mcp.NewTool("query",
			mcp.WithDescription("Search traffic with HTTPQL-like queries. Actions: search (execute), explain (show SQL). Example: req.host.cont:\"example.com\" AND resp.status.eq:200"),
			mcp.WithInputSchema[QueryArgs](),
		),
		backend.queryHandler,
	)

	s.AddTool(
		mcp.NewTool("websocket",
			mcp.WithDescription("WebSocket traffic analysis. Actions: listMessages (by host/connection), search (content search), getConnection (upgrade details), listConnections (all WS connections)"),
			mcp.WithInputSchema[WebSocketArgs](),
		),
		backend.websocketHandler,
	)

	s.AddTool(
		mcp.NewTool("sseClient",
			mcp.WithDescription("SSE (Server-Sent Events) testing client. Actions: connect (open stream), listEvents (get captured events), disconnect (close), listConnections (show all)"),
			mcp.WithInputSchema[SSEClientArgs](),
		),
		backend.sseClientHandler,
	)

	s.AddTool(
		mcp.NewTool("protobuf",
			mcp.WithDescription("Decode protobuf wire format without .proto files. Actions: decode (base64), decodeHex (hex), decodeTraffic (decode gRPC response by request ID)"),
			mcp.WithInputSchema[ProtobufArgs](),
		),
		backend.protobufHandler,
	)

	s.AddTool(
		mcp.NewTool("authzTest",
			mcp.WithDescription("Automated authorization testing. Replay requests with different sessions to find access control issues. Actions: configure, run, results"),
			mcp.WithInputSchema[AuthzTestArgs](),
		),
		backend.authzTestHandler,
	)

	s.AddTool(
		mcp.NewTool("gatherContext",
			mcp.WithDescription("Gather structured intelligence from the active project DB: endpoints, parameters, status distribution, MIME types, error signatures. Pass host to scope to one target (also adds tech stack from _hosts). Omit host for global stats across all hosts (also returns top-50 host breakdown). This is the canonical replacement for the old getTrafficStats / getStatusDistribution / getEndpoints / getParameters tools."),
			mcp.WithInputSchema[GatherContextArgs](),
		),
		backend.gatherContextHandler,
	)

	s.AddTool(
		mcp.NewTool("clusterResponses",
			mcp.WithDescription("Group captured responses by structural fingerprint (status + mime + body shape + length bucket). Use to spot how many DISTINCT response shapes a target produces and find the largest clusters quickly."),
			mcp.WithInputSchema[ClusterResponsesArgs](),
		),
		backend.clusterResponsesHandler,
	)

	s.AddTool(
		mcp.NewTool("findAnomalies",
			mcp.WithDescription("On a given endpoint or host, list responses whose fingerprint differs from the modal one. Surfaces error pages, auth-bypass leakage, format changes, and anything else that doesn't match the dominant response shape."),
			mcp.WithInputSchema[FindAnomaliesArgs](),
		),
		backend.findAnomaliesHandler,
	)

	s.AddTool(
		mcp.NewTool("mirror",
			mcp.WithDescription("Re-fire a captured request (by rowId) or saved template (by templateName) with small mutations applied: method, path, query, appendQuery, setHeaders, removeHeaders, body. Reuses everything else from the baseline so you don't re-emit the full headers/auth/cookies/body each call. Body is JSON-encoded automatically when an object is passed (Content-Type + Content-Length updated). Response body capped at 8KB by default — pass maxBodyBytes:0 for full. Cheap re-probe primitive: 10x token saving over rebuilding sendHttpRequest each iteration. For multi-iteration sweeps, pass `batch:[{path:'/users/1'},{path:'/users/2'},...]` — fires N requests server-side in one MCP round-trip and returns one summary row per iteration. Each batch entry's mutations layer on top of the top-level singleton mutations (singleton = common base, entry = per-iteration override)."),
			mcp.WithInputSchema[MirrorArgs](),
		),
		backend.mirrorHandler,
	)

	s.AddTool(
		mcp.NewTool("mapEndpoints",
			mcp.WithDescription("Build a structured endpoint map for a host: distinct method+pathTemplate tuples (with /users/123 collapsed to /users/{id}), how many times each was seen, status code distribution, and how many distinct response shapes (fingerprints) each produces. One call replaces a sequence of getEndpoints + status-distribution + per-endpoint clustering."),
			mcp.WithInputSchema[MapEndpointsArgs](),
		),
		backend.mapEndpointsHandler,
	)

	s.AddTool(
		mcp.NewTool("probeAuth",
			mcp.WithDescription("Surface the auth boundary of a host from captured traffic: endpoints that carried a credential (Bearer/Basic/Cookie/APIKey/AuthToken), 401/403 denial buckets, and a list of probe candidates ready to replay without auth for an access-control test. Read-only — emits no new traffic; agent decides what to probe next."),
			mcp.WithInputSchema[ProbeAuthArgs](),
		),
		backend.probeAuthHandler,
	)

	// =====================================================================
	// TRAFFIC SEARCH & ANALYSIS (kept individual — high-frequency tools)
	// =====================================================================

	s.AddTool(
		mcp.NewTool("searchTraffic",
			mcp.WithDescription("Search captured traffic by host/path/method/status, or substring/regex on raw request/response. Set regex=true and pass query as a Go regex pattern (regexSource: request|response|both, default both). For per-host stats and endpoint discovery use gatherContext instead."),
			mcp.WithInputSchema[SearchTrafficArgs](),
		),
		backend.searchTrafficHandler,
	)

	s.AddTool(
		mcp.NewTool("generateWordlist",
			mcp.WithDescription("Generate wordlist from discovered paths and/or parameters"),
			mcp.WithInputSchema[GenerateWordlistArgs](),
		),
		backend.generateWordlistHandler,
	)

	// =====================================================================
	// BROWSER TOOLS (pentest-focused, backed by CamoFox)
	// =====================================================================

	s.AddTool(
		mcp.NewTool("browser",
			mcp.WithDescription("Browser tab lifecycle and observation. Actions: open, close, list, navigate, back, forward, refresh, screenshot, snapshot"),
			mcp.WithInputSchema[BrowserArgs](),
		),
		backend.browserHandler,
	)

	s.AddTool(
		mcp.NewTool("browserInteract",
			mcp.WithDescription("Browser page interaction. Actions: click, type, fill, press, scroll, hover, waitForText, waitForSelector"),
			mcp.WithInputSchema[BrowserInteractArgs](),
		),
		backend.browserInteractHandler,
	)

	s.AddTool(
		mcp.NewTool("browserExec",
			mcp.WithDescription("Execute JS and access browser data. Actions: evaluate, getHtml, getLinks, getCookies, setCookies, getConsole, getErrors"),
			mcp.WithInputSchema[BrowserExecArgs](),
		),
		backend.browserExecHandler,
	)

	s.AddTool(
		mcp.NewTool("browserSec",
			mcp.WithDescription("Browser security/admin (CamoFox): auth + XSS testing + server config in one tool. Actions: login|importCookies|exportCookies (auth), verifyAlert|injectPayload|testDomSink|checkCsp|disableCsp|disableCors|disableFrameProtection|restoreDefaults (xss), status|setDisplay|setCamofoxUrl (config). For day-to-day page driving use browser/browserInteract/browserExec instead."),
			mcp.WithInputSchema[ConsolidatedBrowserSecArgs](),
		),
		backend.browserSecHandler,
	)

	// =====================================================================
	// FUZZING
	// =====================================================================

	s.AddTool(
		mcp.NewTool("fuzz",
			mcp.WithDescription("Grammar-based request fuzzing with marker substitution. Actions: configure (set target, request template, payloads), start (begin fuzzing), stop (halt), status (progress), results (get findings)"),
			mcp.WithInputSchema[FuzzArgs](),
		),
		backend.fuzzHandler,
	)

	sseServer := mcpserver.NewSSEServer(s,
		mcpserver.WithStaticBasePath("/mcp"),
		mcpserver.WithKeepAlive(true),
	)

	backend.MCP = &MCP{
		server:    s,
		sseServer: sseServer,
		active:    true,
	}
}

// ---------------------------------------------------------------------------
// HTTP endpoints
// ---------------------------------------------------------------------------

func (backend *Backend) MCPEndpoint(e *echo.Echo) {
	backend.mcpInit()

	// MCP token authentication middleware
	requireMCPAuth := func(c echo.Context) error {
		if backend.Config.MCPToken == "" {
			return nil // no token configured, allow all
		}
		auth := c.Request().Header.Get("Authorization")
		if auth == "" {
			return c.JSON(http.StatusUnauthorized, map[string]any{"error": "Authorization header required"})
		}
		if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != backend.Config.MCPToken {
			return c.JSON(http.StatusUnauthorized, map[string]any{"error": "invalid bearer token"})
		}
		return nil
	}

	e.POST("/mcp/start", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}
		if backend.MCP != nil && backend.MCP.active {
			return c.JSON(http.StatusOK, map[string]any{"message": "MCP server already active"})
		}
		backend.mcpInit()
		log.Println("[MCP] Server started")
		return c.JSON(http.StatusOK, map[string]any{"message": "MCP server started"})
	})

	e.POST("/mcp/stop", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}
		if backend.MCP == nil || !backend.MCP.active {
			return c.JSON(http.StatusOK, map[string]any{"message": "MCP server already stopped"})
		}
		backend.MCP.active = false
		log.Println("[MCP] Server stopped")
		return c.JSON(http.StatusOK, map[string]any{"message": "MCP server stopped"})
	})

	e.GET("/mcp/health", func(c echo.Context) error {
		if backend.MCP == nil || !backend.MCP.active {
			return c.JSON(http.StatusOK, map[string]any{"active": false})
		}

		tools := backend.MCP.server.ListTools()
		toolNames := make([]string, 0, len(tools))
		for name := range tools {
			toolNames = append(toolNames, name)
		}

		return c.JSON(http.StatusOK, map[string]any{
			"active":      true,
			"status":      "ok",
			"server":      "lorg",
			"version":     version.CURRENT_BACKEND_VERSION,
			"tools":       toolNames,
			"connections": backend.MCP.conns.Load(),
		})
	})

	e.GET("/mcp/listtools", func(c echo.Context) error {
		if backend.MCP == nil || !backend.MCP.active {
			return c.JSON(http.StatusServiceUnavailable, map[string]any{"error": "MCP server not active"})
		}

		tools := backend.MCP.server.ListTools()
		type toolInfo struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		result := make([]toolInfo, 0, len(tools))
		for name, t := range tools {
			result = append(result, toolInfo{
				Name:        name,
				Description: t.Tool.Description,
			})
		}

		return c.JSON(http.StatusOK, map[string]any{
			"tools": result,
			"count": len(result),
		})
	})

	e.GET("/mcp/sse", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}
		if err := requireMCPAuth(c); err != nil {
			return err
		}
		if backend.MCP == nil || !backend.MCP.active {
			return c.JSON(http.StatusServiceUnavailable, map[string]any{"error": "MCP server not active"})
		}
		backend.MCP.conns.Add(1)
		defer backend.MCP.conns.Add(-1)
		backend.MCP.sseServer.SSEHandler().ServeHTTP(c.Response(), c.Request())
		return nil
	})

	e.POST("/mcp/message", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}
		if err := requireMCPAuth(c); err != nil {
			return err
		}
		if backend.MCP == nil || !backend.MCP.active {
			return c.JSON(http.StatusServiceUnavailable, map[string]any{"error": "MCP server not active"})
		}
		backend.MCP.sseServer.MessageHandler().ServeHTTP(c.Response(), c.Request())
		return nil
	})

	e.POST("/mcp/setup/claude", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}
		var body struct {
			ClaudeMD string `json:"claude_md"`
		}
		if err := c.Bind(&body); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]any{"error": "Invalid request body"})
		}

		cwd, _ := os.Getwd()
		mcpSseURL := fmt.Sprintf("http://%s/mcp/sse", backend.Config.HostAddr)

		mcpSettings := map[string]any{
			"mcpServers": map[string]any{
				"lorg": map[string]any{
					"type": "sse",
					"url":  mcpSseURL,
				},
			},
		}

		mcpJSON, _ := json.MarshalIndent(mcpSettings, "", "  ")
		save.WriteFile(".mcp.json", mcpJSON)

		claudeMDPath := filepath.Join(cwd, "CLAUDE.md")
		claudeContent := body.ClaudeMD
		save.WriteFile(claudeMDPath, []byte(claudeContent))

		return c.JSON(http.StatusOK, map[string]any{
			"success": true,
			"message": "Claude Code integration configured",
		})
	})

	log.Println("[MCP] Endpoints registered at /mcp/")
}
