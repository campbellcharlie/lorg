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
		mcp.NewTool("hostPrintSitemap",
			mcp.WithDescription("Get the sitemap for a host"),
			mcp.WithInputSchema[HostPrintSitemapArgs](),
		),
		backend.hostPrintSitemapHandler,
	)

	s.AddTool(
		mcp.NewTool("hostPrintRowsInDetails",
			mcp.WithDescription("Get the table for a host"),
			mcp.WithInputSchema[HostPrintRowsArgs](),
		),
		backend.hostPrintRowsInDetailsHandler,
	)

	s.AddTool(
		mcp.NewTool("listHosts",
			mcp.WithDescription("List all hosts with their technologies (as names) and labels (as names)"),
			mcp.WithInputSchema[ListHostsArgs](),
		),
		backend.listHostsHandler,
	)

	s.AddTool(
		mcp.NewTool("getHostInfo",
			mcp.WithDescription("Get detailed info for a specific host by ID, including technologies (as names), labels (as names), and notes"),
			mcp.WithInputSchema[GetHostInfoArgs](),
		),
		backend.getHostInfoHandler,
	)

	s.AddTool(
		mcp.NewTool("getNoteForHost",
			mcp.WithDescription("Get the note for a host"),
			mcp.WithInputSchema[GetNoteForHostArgs](),
		),
		backend.getNoteForHostHandler,
	)

	s.AddTool(
		mcp.NewTool("setNoteForHost",
			mcp.WithDescription("Set the note for a host"),
			mcp.WithInputSchema[SetNoteForHostArgs](),
		),
		backend.setNoteForHostHandler,
	)

	s.AddTool(
		mcp.NewTool("modifyHostLabels",
			mcp.WithDescription("Add or remove labels from a host"),
			mcp.WithInputSchema[ModifyHostLabelsArgs](),
		),
		backend.modifyHostLabelsHandler,
	)

	s.AddTool(
		mcp.NewTool("modifyHostNotes",
			mcp.WithDescription("Add, update, or remove notes for a host"),
			mcp.WithInputSchema[ModifyHostNotesArgs](),
		),
		backend.modifyHostNotesHandler,
	)

	// =====================================================================
	// REQUEST SENDING
	// =====================================================================

	s.AddTool(
		mcp.NewTool("sendRequest",
			mcp.WithDescription("Send a raw HTTP request. Mind the terminating \\r\\n\\r\\n and content length."),
			mcp.WithInputSchema[SendRequestArgs](),
		),
		backend.sendRequestHandler,
	)

	s.AddTool(
		mcp.NewTool("sendHttpRequest",
			mcp.WithDescription("Send a structured HTTP request with session injection, CSRF handling, redirect following, and response extraction. The primary request tool."),
			mcp.WithInputSchema[SendHttpRequestArgs](),
		),
		backend.sendHttpRequestHandler,
	)

	s.AddTool(
		mcp.NewTool("replayFromDb",
			mcp.WithDescription("Replay a request from the project SQLite DB by request_id, with optional header/body modifications"),
			mcp.WithInputSchema[ReplayFromDbArgs](),
		),
		backend.replayFromDbHandler,
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
		mcp.NewTool("interceptToggle",
			mcp.WithDescription("Enable or disable request/response interception on a proxy. When disabled, all pending intercepts are automatically forwarded"),
			mcp.WithInputSchema[InterceptToggleArgs](),
		),
		backend.interceptToggleHandler,
	)

	s.AddTool(
		mcp.NewTool("interceptPrintRowsInDetails",
			mcp.WithDescription("List intercepted requests/responses for a proxy with full metadata (host, port, method, path, status, headers)"),
			mcp.WithInputSchema[InterceptReadArgs](),
		),
		backend.interceptReadHandler,
	)

	s.AddTool(
		mcp.NewTool("interceptGetRawRequestAndResponse",
			mcp.WithDescription("Get the raw HTTP request and response strings for a specific intercepted record, for reading or editing before forwarding"),
			mcp.WithInputSchema[InterceptGetRawArgs](),
		),
		backend.interceptGetRawHandler,
	)

	s.AddTool(
		mcp.NewTool("interceptAction",
			mcp.WithDescription("Take action on a pending intercept: forward (pass through, optionally with edits) or drop (block the request/response)"),
			mcp.WithInputSchema[InterceptActionArgs](),
		),
		backend.interceptActionHandler,
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

	s.AddTool(
		mcp.NewTool("proxyScreenshot",
			mcp.WithDescription("Capture a screenshot from Chrome browser attached to a proxy instance via Chrome DevTools Protocol, wait after calling the tool"),
			mcp.WithInputSchema[ProxyScreenshotArgs](),
		),
		backend.proxyScreenshotHandler,
	)

	s.AddTool(
		mcp.NewTool("proxyClick",
			mcp.WithDescription("Click an element on the page using Chrome browser attached to a proxy instance via Chrome DevTools Protocol"),
			mcp.WithInputSchema[ProxyClickArgs](),
		),
		backend.proxyClickHandler,
	)

	s.AddTool(
		mcp.NewTool("proxyElements",
			mcp.WithDescription("Extract information about all interactive elements on the page (buttons, links, inputs, textareas, selects). Returns unique CSS selectors for each element using nth-of-type paths"),
			mcp.WithInputSchema[ProxyElementsArgs](),
		),
		backend.proxyElementsHandler,
	)

	s.AddTool(
		mcp.NewTool("proxyType",
			mcp.WithDescription("Type text into a form field (input, textarea) on the page. Clicks to focus, optionally clears existing value, then dispatches real key events"),
			mcp.WithInputSchema[ProxyTypeArgs](),
		),
		backend.proxyTypeHandler,
	)

	s.AddTool(
		mcp.NewTool("proxyEval",
			mcp.WithDescription("Execute arbitrary JavaScript in the page context and return the result. Useful for setting values, reading DOM state, triggering events, or any operation not covered by other tools"),
			mcp.WithInputSchema[ProxyEvalArgs](),
		),
		backend.proxyEvalHandler,
	)

	s.AddTool(
		mcp.NewTool("proxyWaitForSelector",
			mcp.WithDescription("Wait for a CSS selector to become visible on the page. Useful for SPA transitions where waitForNavigation doesn't work"),
			mcp.WithInputSchema[ProxyWaitForSelectorArgs](),
		),
		backend.proxyWaitForSelectorHandler,
	)

	// --- Chrome Tab tools ---

	s.AddTool(
		mcp.NewTool("proxyListTabs",
			mcp.WithDescription("Lists all open tabs in the Chrome browser attached to a proxy instance"),
			mcp.WithInputSchema[ProxyListTabsArgs](),
		),
		backend.proxyListTabsHandler,
	)

	s.AddTool(
		mcp.NewTool("proxyOpenTab",
			mcp.WithDescription("Opens a new tab in the Chrome browser attached to a proxy instance"),
			mcp.WithInputSchema[ProxyOpenTabArgs](),
		),
		backend.proxyOpenTabHandler,
	)

	s.AddTool(
		mcp.NewTool("proxyNavigateTab",
			mcp.WithDescription("Navigates a specific tab (or the active tab) to a URL with configurable wait conditions"),
			mcp.WithInputSchema[ProxyNavigateTabArgs](),
		),
		backend.proxyNavigateTabHandler,
	)

	s.AddTool(
		mcp.NewTool("proxyActivateTab",
			mcp.WithDescription("Switches focus to a specific tab, making it the active tab in Chrome"),
			mcp.WithInputSchema[ProxyActivateTabArgs](),
		),
		backend.proxyActivateTabHandler,
	)

	s.AddTool(
		mcp.NewTool("proxyCloseTab",
			mcp.WithDescription("Closes a specific tab in Chrome"),
			mcp.WithInputSchema[ProxyCloseTabArgs](),
		),
		backend.proxyCloseTabHandler,
	)

	s.AddTool(
		mcp.NewTool("proxyReloadTab",
			mcp.WithDescription("Reloads a specific tab or the active tab, optionally bypassing cache"),
			mcp.WithInputSchema[ProxyReloadTabArgs](),
		),
		backend.proxyReloadTabHandler,
	)

	s.AddTool(
		mcp.NewTool("proxyGoBack",
			mcp.WithDescription("Navigates back in the browser history for a specific tab or the active tab"),
			mcp.WithInputSchema[ProxyGoBackArgs](),
		),
		backend.proxyGoBackHandler,
	)

	s.AddTool(
		mcp.NewTool("proxyGoForward",
			mcp.WithDescription("Navigates forward in the browser history for a specific tab or the active tab"),
			mcp.WithInputSchema[ProxyGoForwardArgs](),
		),
		backend.proxyGoForwardHandler,
	)

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
		mcp.NewTool("extract",
			mcp.WithDescription("Extract data from content. Actions: regex (with capture groups), jsonPath (dot-notation), between (delimiters)"),
			mcp.WithInputSchema[ConsolidatedExtractArgs](),
		),
		backend.extractHandler,
	)

	s.AddTool(
		mcp.NewTool("analyze",
			mcp.WithDescription("Analyze HTTP responses. Actions: response (security headers, cookies, tech), variations (invariant vs varying), keywords (track presence)"),
			mcp.WithInputSchema[ConsolidatedAnalyzeArgs](),
		),
		backend.analyzeHandler,
	)

	s.AddTool(
		mcp.NewTool("oob",
			mcp.WithDescription("Out-of-band callback server for blind vulnerability detection. Actions: start, stop, generatePayload, pollInteractions, clearInteractions"),
			mcp.WithInputSchema[OOBArgs](),
		),
		backend.oobHandler,
	)

	s.AddTool(
		mcp.NewTool("compare",
			mcp.WithDescription("Diff HTTP responses. Actions: responses (raw strings), byId (PocketBase activeIDs), structural (header-by-header + body), jsonDiff (JSON tree diff)"),
			mcp.WithInputSchema[ConsolidatedCompareArgs](),
		),
		backend.compareHandler,
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
			mcp.WithDescription("Gather structured intelligence about a target host from captured traffic: endpoints, parameters, tech stack, status distribution, error signatures"),
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
			mcp.WithDescription("Re-fire a captured request (by rowId) or saved template (by templateName) with small mutations applied: method, path, query, appendQuery, setHeaders, removeHeaders, body. Reuses everything else from the baseline so you don't re-emit the full headers/auth/cookies/body each call. Body is JSON-encoded automatically when an object is passed (Content-Type + Content-Length updated). Response body capped at 8KB by default — pass maxBodyBytes:0 for full. Cheap re-probe primitive: 10x token saving over rebuilding sendHttpRequest each iteration."),
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
			mcp.WithDescription("Search traffic by host, path, method, status, or raw content"),
			mcp.WithInputSchema[SearchTrafficArgs](),
		),
		backend.searchTrafficHandler,
	)

	s.AddTool(
		mcp.NewTool("searchTrafficRegex",
			mcp.WithDescription("Search traffic with regex patterns in request/response content"),
			mcp.WithInputSchema[SearchTrafficRegexArgs](),
		),
		backend.searchTrafficRegexHandler,
	)

	s.AddTool(
		mcp.NewTool("getTrafficStats",
			mcp.WithDescription("Get aggregate traffic statistics: total requests, hosts, methods"),
		),
		backend.getTrafficStatsHandler,
	)

	s.AddTool(
		mcp.NewTool("getStatusDistribution",
			mcp.WithDescription("Get HTTP response status code distribution"),
			mcp.WithInputSchema[GetStatusDistributionArgs](),
		),
		backend.getStatusDistributionHandler,
	)

	s.AddTool(
		mcp.NewTool("getEndpoints",
			mcp.WithDescription("Get unique URL endpoints discovered in traffic"),
			mcp.WithInputSchema[GetEndpointsArgs](),
		),
		backend.getEndpointsHandler,
	)

	s.AddTool(
		mcp.NewTool("getParameters",
			mcp.WithDescription("Get unique query parameters extracted from traffic"),
			mcp.WithInputSchema[GetParametersArgs](),
		),
		backend.getParametersHandler,
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
		mcp.NewTool("browserXss",
			mcp.WithDescription("XSS verification and security bypass. Actions: verifyAlert (XSS proof), injectPayload, testDomSink, checkCsp, disableCsp, disableCors, disableFrameProtection, restoreDefaults"),
			mcp.WithInputSchema[BrowserXssArgs](),
		),
		backend.browserXssHandler,
	)

	s.AddTool(
		mcp.NewTool("browserAuth",
			mcp.WithDescription("Browser authentication and cookie management. Actions: login (auto-detects form fields), importCookies, exportCookies"),
			mcp.WithInputSchema[BrowserAuthArgs](),
		),
		backend.browserAuthHandler,
	)

	s.AddTool(
		mcp.NewTool("browserConfig",
			mcp.WithDescription("CamoFox browser server configuration. Actions: status, setDisplay (headless/headed/virtual), setCamofoxUrl"),
			mcp.WithInputSchema[BrowserConfigArgs](),
		),
		backend.browserConfigHandler,
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
