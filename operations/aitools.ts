import { z } from "zod";

// ---------------------------------------------------------------------------
// AI Tool definitions for the 42 currently-registered MCP tools.
//
// Source of truth: apps/app/mcp.go and the *Args structs in apps/app/mcp_*.go.
// This file is a standalone spec/tracker — it has no consumers in the repo.
// For dispatcher tools (host, intercept, responseAnalysis, browserSec, etc.)
// the first parameter is `action: z.enum([...])` listing every valid action.
// Where an arg list is large, we keep the high-signal fields and add a
// trailing comment that the parameter list is truncated.
// ---------------------------------------------------------------------------

export const toolsData: Record<string,
    {
        title: string,
        icon?: string,
        description: string,
        parameters: z.ZodObject<any, any, any, any>,
        inputExample: any,
        outputExample?: any,
    }> = {

    // === System ===
    'lorgStatus': {
        title: 'lorg Status',
        icon: 'ri:settings-2-line',
        description: 'Check if the lorg is active',
        parameters: z.object({}),
        inputExample: {},
        outputExample: { active: true, version: '2026.3.9' },
    },

    // === Project / state ===
    'project': {
        title: 'Project Manager',
        icon: 'ri:folder-settings-line',
        description: 'Manage project settings, SQLite DB, traffic logging, and privacy. Actions: setup, info, setName, export, setLogging, setRedactionMode, getRedactionMode',
        parameters: z.object({
            action: z.enum(['setup', 'info', 'setName', 'export', 'setLogging', 'setRedactionMode', 'getRedactionMode']),
            name: z.string().optional().describe('Project name (setup, setName)'),
            dbDir: z.string().optional().describe('Directory for SQLite DB files (setup)'),
            outputPath: z.string().optional().describe('Output path for export (export)'),
            projectName: z.string().optional().describe('Project name for export metadata (export)'),
            hostFilter: z.string().optional().describe('Only export traffic for this host (export)'),
            enabled: z.boolean().optional().describe('Enable or disable traffic logging (setLogging)'),
            sources: z.array(z.string()).optional().describe('Which sources to log: proxy, repeater, mcp, template, all (setLogging)'),
            redactionMode: z.string().optional().describe('Redaction mode: off, balanced, strict (setRedactionMode)'),
        }),
        inputExample: { action: 'info' },
        outputExample: {},
    },

    // === Proxy lifecycle ===
    'proxyList': {
        title: 'List Proxies',
        icon: 'ri:list-check',
        description: 'Get a list of all running proxy instances with their status, browser type, and configuration',
        parameters: z.object({}),
        inputExample: {},
        outputExample: { proxies: [], count: 0 },
    },

    'proxyStart': {
        title: 'Start Proxy',
        icon: 'ri:play-circle-line',
        description: 'Start a new proxy instance with optional browser attachment',
        parameters: z.object({
            name: z.string().optional().describe('Optional label for the proxy instance'),
            browser: z.enum(['firefox', 'none']).optional().describe('Browser to attach: firefox (CamoFox) or none (default)'),
        }),
        inputExample: { name: 'recon', browser: 'firefox' },
        outputExample: { id: '______________1', listenAddr: '127.0.0.1:8080' },
    },

    'proxyStop': {
        title: 'Stop Proxy',
        icon: 'ri:stop-circle-line',
        description: 'Stop a running proxy instance by ID, or stop all proxies if no ID is provided',
        parameters: z.object({
            id: z.string().optional().describe('Proxy ID to stop. Omit to stop all running proxies'),
        }),
        inputExample: { id: '______________1' },
        outputExample: { message: 'Proxy stopped' },
    },

    // === Intercept (consolidated) ===
    'intercept': {
        title: 'Intercept Manager',
        icon: 'ri:pause-circle-line',
        description: 'Manage proxy interception in one tool. Actions: toggle (enable/disable on a proxy), list (intercepted rows), getRaw (raw req/resp for one record), forward (pass through, optional edits), drop (block). Replaces interceptToggle/interceptPrintRowsInDetails/interceptGetRawRequestAndResponse/interceptAction.',
        parameters: z.object({
            action: z.enum(['toggle', 'list', 'getRaw', 'forward', 'drop']),
            id: z.string().optional().describe('Proxy ID (toggle) or intercept record ID (getRaw, forward, drop)'),
            enable: z.boolean().optional().describe('true to enable, false to disable (toggle)'),
            proxyId: z.string().optional().describe('Proxy ID to list intercepted rows for (list)'),
            isReqEdited: z.boolean().optional().describe('True if the request was edited (forward)'),
            isRespEdited: z.boolean().optional().describe('True if the response was edited (forward)'),
            reqEdited: z.string().optional().describe('Raw edited HTTP request (forward, if isReqEdited)'),
            respEdited: z.string().optional().describe('Raw edited HTTP response (forward, if isRespEdited)'),
        }),
        inputExample: { action: 'toggle', id: '______________1', enable: true },
        outputExample: {},
    },

    'matchReplace': {
        title: 'Match & Replace',
        icon: 'ri:find-replace-line',
        description: 'Manage proxy match & replace rules. Auto-modify HTTP requests/responses as they pass through the proxy. Actions: add, list, remove, enable, disable, reload',
        parameters: z.object({
            action: z.enum(['add', 'list', 'remove', 'enable', 'disable', 'reload']),
            id: z.string().optional().describe('Rule ID (remove/enable/disable)'),
            type: z.string().optional().describe('Rule type: request_header, request_body, response_header, response_body'),
            match: z.string().optional().describe('Regex pattern to match'),
            replace: z.string().optional().describe('Replacement string (supports $1 capture groups)'),
            scope: z.string().optional().describe('Host filter (empty = all hosts)'),
            comment: z.string().optional().describe('Description of what this rule does'),
        }),
        inputExample: { action: 'list' },
        outputExample: {},
    },

    // === Host inventory (consolidated) ===
    'host': {
        title: 'Host Manager',
        icon: 'ri:server-line',
        description: 'Host inventory + per-host details in one tool. Actions: list (paged hosts with tech/labels), info (one host), sitemap (URL tree under path), rows (request rows for a host), getNote/setNote (legacy single note), modifyLabels (add/remove/toggle), modifyNotes (add/update/remove). Replaces listHosts/getHostInfo/hostPrintSitemap/hostPrintRowsInDetails/getNoteForHost/setNoteForHost/modifyHostLabels/modifyHostNotes.',
        parameters: z.object({
            action: z.enum(['list', 'info', 'sitemap', 'rows', 'getNote', 'setNote', 'modifyLabels', 'modifyNotes']),
            host: z.string().optional().describe('Host (info, sitemap, rows, getNote, setNote, modifyLabels, modifyNotes). For modify*, include the protocol (e.g. http://example.com).'),
            search: z.string().optional().describe('Search filter for host names (list)'),
            page: z.number().optional().describe('Page number, 1-indexed (list, rows)'),
            path: z.string().optional().describe('Path prefix to scope the sitemap (sitemap)'),
            depth: z.number().optional().describe('Sitemap depth, -1 for full (sitemap)'),
            filter: z.string().optional().describe("Substring filter for the host's request rows (rows)"),
            edit: z.array(z.object({
                index: z.number().describe('Line index to edit'),
                line: z.string().optional().describe('New content; use [delete] to remove'),
            })).optional().describe('Per-line edits for the host note (setNote)'),
            labels: z.array(z.object({
                action: z.enum(['add', 'remove', 'toggle']),
                name: z.string(),
                color: z.string().optional(),
                type: z.string().optional(),
            })).optional().describe('Label actions (modifyLabels)'),
            notes: z.array(z.object({
                action: z.enum(['add', 'update', 'remove']),
                text: z.string().optional(),
                index: z.number().optional(),
            })).optional().describe('Note actions (modifyNotes)'),
        }),
        inputExample: { action: 'list', search: 'example', page: 1 },
        outputExample: {},
    },

    // === Search & query ===
    'searchTraffic': {
        title: 'Search Traffic',
        icon: 'ri:search-eye-line',
        description: 'Search captured traffic by host/path/method/status, or substring/regex on raw request/response. Set regex=true and pass query as a Go regex pattern (regexSource: request|response|both, default both). For per-host stats and endpoint discovery use gatherContext instead.',
        parameters: z.object({
            host: z.string().optional().describe('Filter by host (substring match)'),
            path: z.string().optional().describe('Filter by URL path substring'),
            method: z.string().optional().describe('Filter by HTTP method'),
            status: z.number().optional().describe('Filter by response status code'),
            query: z.string().optional().describe('Search in request/response raw content (substring by default; regex when regex=true)'),
            regex: z.boolean().optional().describe('Treat query as a Go regex pattern instead of a literal substring'),
            regexSource: z.string().optional().describe('For regex queries, which side to search: request, response, or both (default: both)'),
            limit: z.number().describe('Max results (max 200)'),
            offset: z.number().optional().describe('Offset for pagination (cursor-style)'),
        }),
        inputExample: { host: 'example.com', limit: 50 },
        outputExample: {},
    },

    'query': {
        title: 'HTTPQL Query',
        icon: 'ri:terminal-box-line',
        description: 'Search traffic with HTTPQL-like queries. Actions: search (execute), explain (show SQL). Example: req.host.cont:"example.com" AND resp.status.eq:200',
        parameters: z.object({
            action: z.enum(['search', 'explain']),
            query: z.string().describe('HTTPQL-like query string'),
            limit: z.number().optional().describe('Max results (default 100)'),
        }),
        inputExample: { action: 'search', query: 'req.host.cont:"example.com" AND resp.status.eq:200', limit: 100 },
        outputExample: {},
    },

    'getRequestResponseFromID': {
        title: 'Get Request/Response',
        icon: 'ri:file-list-3-line',
        description: 'Get the active request and response for active ID',
        parameters: z.object({
            activeID: z.string().describe('The active ID'),
        }),
        inputExample: { activeID: '476' },
        outputExample: {},
    },

    // === Recon / context ===
    'gatherContext': {
        title: 'Gather Context',
        icon: 'ri:bar-chart-box-line',
        description: 'Gather structured intelligence from the active project DB: endpoints, parameters, status distribution, MIME types, error signatures. Pass host to scope to one target (also adds tech stack from _hosts). Omit host for global stats across all hosts (also returns top-50 host breakdown). Canonical replacement for getTrafficStats / getStatusDistribution / getEndpoints / getParameters.',
        parameters: z.object({
            host: z.string().optional().describe('Target hostname. Omit or empty for global stats across all hosts.'),
            limit: z.number().optional().describe('Max traffic entries to analyze (default 500)'),
        }),
        inputExample: { host: 'example.com', limit: 500 },
        outputExample: {},
    },

    'mapEndpoints': {
        title: 'Map Endpoints',
        icon: 'ri:road-map-line',
        description: 'Build a structured endpoint map for a host: distinct method+pathTemplate tuples (with /users/123 collapsed to /users/{id}), how many times each was seen, status code distribution, and how many distinct response shapes (fingerprints) each produces. One call replaces a sequence of getEndpoints + status-distribution + per-endpoint clustering.',
        parameters: z.object({
            host: z.string().describe('Target hostname (LIKE substring match)'),
            limit: z.number().optional().describe('Max endpoints to return (default 100)'),
        }),
        inputExample: { host: 'api.example.com', limit: 100 },
        outputExample: {},
    },

    'clusterResponses': {
        title: 'Cluster Responses',
        icon: 'ri:bubble-chart-line',
        description: 'Group captured responses by structural fingerprint (status + mime + body shape + length bucket). Use to spot how many DISTINCT response shapes a target produces and find the largest clusters quickly.',
        parameters: z.object({
            host: z.string().optional().describe('Hostname filter (LIKE substring match)'),
            method: z.string().optional().describe('HTTP method filter (e.g. GET, POST). Optional, exact match.'),
            path: z.string().optional().describe('Path filter (LIKE substring match)'),
            limit: z.number().optional().describe('Max clusters to return (default 50)'),
        }),
        inputExample: { host: 'example.com', limit: 50 },
        outputExample: {},
    },

    'findAnomalies': {
        title: 'Find Anomalies',
        icon: 'ri:error-warning-line',
        description: "On a given endpoint or host, list responses whose fingerprint differs from the modal one. Surfaces error pages, auth-bypass leakage, format changes, and anything else that doesn't match the dominant response shape.",
        parameters: z.object({
            host: z.string().describe('Hostname (LIKE substring match)'),
            method: z.string().optional().describe('HTTP method filter (e.g. GET). Optional, exact match.'),
            path: z.string().optional().describe('Path filter (LIKE substring match). Optional.'),
            limit: z.number().optional().describe('Max anomalous rows to return (default 25)'),
        }),
        inputExample: { host: 'example.com', path: '/api/users', limit: 25 },
        outputExample: {},
    },

    'responseAnalysis': {
        title: 'Response Analysis',
        icon: 'ri:scan-2-line',
        description: 'Inspect, extract from, or diff HTTP responses. Replaces extract/analyze/compare. Actions: analyzeResponse|analyzeVariations|analyzeKeywords (parse headers/cookies/tech, find invariants, track keywords across many responses), extractRegex|extractJsonPath|extractBetween (pull values out of content), diffResponses|diffById|diffStructural|diffJson (compare two responses by raw text, captured row id, structure, or JSON tree).',
        parameters: z.object({
            action: z.enum([
                'analyzeResponse', 'analyzeVariations', 'analyzeKeywords',
                'extractRegex', 'extractJsonPath', 'extractBetween',
                'diffResponses', 'diffById', 'diffStructural', 'diffJson',
            ]),
            response: z.string().optional().describe('Raw HTTP response (analyzeResponse)'),
            responses: z.array(z.string()).optional().describe('Multiple raw HTTP responses (analyzeVariations, analyzeKeywords)'),
            keywords: z.array(z.string()).optional().describe('Keywords to search for (analyzeKeywords)'),
            content: z.string().optional().describe('Content to search in (extract* actions)'),
            pattern: z.string().optional().describe('Regex pattern (extractRegex)'),
            maxMatches: z.number().optional().describe('Max matches to return (extractRegex, extractBetween)'),
            group: z.number().optional().describe('Capture group (extractRegex, 0=full match)'),
            path: z.string().optional().describe('Dot-notation JSON path (extractJsonPath)'),
            startDelimiter: z.string().optional().describe('Start delimiter (extractBetween)'),
            endDelimiter: z.string().optional().describe('End delimiter (extractBetween)'),
            response1: z.string().optional().describe('First raw HTTP response (diffResponses, diffStructural) or JSON string (diffJson)'),
            response2: z.string().optional().describe('Second raw HTTP response (diffResponses, diffStructural) or JSON string (diffJson)'),
            id1: z.string().optional().describe('First captured request ID (diffById)'),
            id2: z.string().optional().describe('Second captured request ID (diffById)'),
            ignoreHeaders: z.array(z.string()).optional().describe('Header names to ignore (diffResponses, diffById, diffStructural)'),
        }),
        inputExample: { action: 'analyzeResponse', response: 'HTTP/1.1 200 OK\r\n...' },
        outputExample: {},
    },

    'probeAuth': {
        title: 'Probe Auth Boundary',
        icon: 'ri:shield-keyhole-line',
        description: 'Surface the auth boundary of a host from captured traffic: endpoints that carried a credential (Bearer/Basic/Cookie/APIKey/AuthToken), 401/403 denial buckets, and a list of probe candidates ready to replay without auth for an access-control test. Read-only — emits no new traffic; agent decides what to probe next.',
        parameters: z.object({
            host: z.string().describe('Target hostname (LIKE substring match)'),
            limit: z.number().optional().describe('Max probe candidates to return (default 25)'),
        }),
        inputExample: { host: 'example.com', limit: 25 },
        outputExample: {},
    },

    // === HTTP request emitters ===
    'sendHttpRequest': {
        title: 'Send HTTP Request',
        icon: 'ri:send-plane-line',
        description: 'Send a structured HTTP request: method, url, headers, body, with session injection, CSRF handling, redirect following, and optional regex extraction from the response. The primary request tool — use this for one-off requests where you have all the parameters. For re-firing a captured request with small mutations, use mirror() instead (much cheaper). For raw byte control over the request line, use sendRaw.',
        parameters: z.object({
            method: z.string().describe('HTTP method (GET, POST, PUT, DELETE, PATCH, etc.)'),
            url: z.string().describe('Full URL (e.g. http://example.com:8000/path?q=1)'),
            headers: z.record(z.string()).optional().describe('Additional request headers'),
            body: z.string().optional().describe('Request body'),
            injectSession: z.boolean().optional().describe('Auto-inject active session cookies, CSRF token, custom headers'),
            captureSession: z.boolean().optional().describe('Auto-extract Set-Cookie and CSRF tokens from response into active session'),
            followRedirects: z.boolean().optional().describe('Follow 3xx redirects (up to 10 hops)'),
            extractRegex: z.string().optional().describe('Regex to extract from response body'),
            extractGroup: z.number().optional().describe('Capture group for extraction (default: 1)'),
            bodyOnly: z.boolean().optional().describe('Return only response body, not headers'),
            maxBodyLength: z.number().optional().describe('Truncate response body to this many characters (0 = no limit)'),
            headersOnly: z.boolean().optional().describe('Return only response headers, no body. Overrides bodyOnly.'),
            note: z.string().optional().describe('Note to attach to request'),
        }),
        inputExample: { method: 'GET', url: 'https://example.com/api/users' },
        outputExample: {},
    },

    'sendRaw': {
        title: 'Send Raw Bytes',
        icon: 'ri:code-s-slash-line',
        description: 'Send raw TCP/TLS bytes or HTTP/2 sequences. Actions: tcp, tls, h2Sequence',
        parameters: z.object({
            action: z.enum(['tcp', 'tls', 'h2Sequence']),
            host: z.string().optional().describe('Target hostname'),
            port: z.number().optional().describe('Target port'),
            segments: z.array(z.any()).optional().describe('Data segments to send in order (tcp, tls)'),
            connectTimeoutMs: z.number().optional(),
            readTimeoutMs: z.number().optional(),
            maxReadBytes: z.number().optional(),
            previewBytes: z.number().optional().describe('Bytes to include as UTF-8 preview (default: 500)'),
            alpnProtocols: z.array(z.string()).optional().describe('ALPN protocols (tls)'),
            insecureSkipVerify: z.boolean().optional().describe('Skip TLS certificate verification (tls, h2Sequence)'),
            requests: z.array(z.any()).optional().describe('HTTP requests to send sequentially (h2Sequence)'),
        }),
        inputExample: { action: 'tcp', host: 'example.com', port: 80, segments: [{ data: 'GET / HTTP/1.1\r\n\r\n' }] },
        outputExample: {},
    },

    'mirror': {
        title: 'Mirror Request',
        icon: 'ri:repeat-line',
        description: "Re-fire a captured request (by rowId) or saved template (by templateName) with small mutations applied: method, path, query, appendQuery, setHeaders, removeHeaders, body. Reuses everything else from the baseline so you don't re-emit the full headers/auth/cookies/body each call. Body is JSON-encoded automatically when an object is passed (Content-Type + Content-Length updated). Response body capped at 8KB by default — pass maxBodyBytes:0 for full. Cheap re-probe primitive: 10x token saving over rebuilding sendHttpRequest each iteration.",
        parameters: z.object({
            rowId: z.string().optional().describe('Captured traffic row id to clone (preferred)'),
            templateName: z.string().optional().describe('Saved template name to clone (alternative to rowId)'),
            method: z.string().optional().describe('Replace HTTP method (e.g. PUT, DELETE)'),
            path: z.string().optional().describe('Replace URL path. Query string preserved unless query is also set.'),
            query: z.string().optional().describe('Replace URL query string (without ?). Use empty string to drop the query entirely.'),
            appendQuery: z.record(z.string()).optional().describe('Add or overwrite individual query params (additive)'),
            setHeaders: z.record(z.string()).optional().describe('Add or replace specific headers (case-insensitive name match)'),
            removeHeaders: z.array(z.string()).optional().describe('Drop these headers entirely (case-insensitive)'),
            body: z.any().optional().describe('Replace body. Object → JSON-encoded + CT/CL updated. String → verbatim.'),
            host: z.string().optional().describe('Override target host (defaults to baseline)'),
            port: z.number().optional().describe('Override target port (defaults to baseline)'),
            tls: z.boolean().optional().describe('Override TLS flag (defaults to baseline)'),
            http2: z.boolean().optional().describe('Override HTTP version (defaults to baseline)'),
            note: z.string().optional().describe('Note to attach to the saved row'),
            maxBodyBytes: z.number().optional().describe('Cap response body in returned summary (default 8192). Use 0 for no cap.'),
        }),
        inputExample: { rowId: '123', method: 'PUT', appendQuery: { admin: 'true' } },
        outputExample: {},
    },

    'exportCurl': {
        title: 'Export as curl',
        icon: 'ri:terminal-line',
        description: 'Export a stored request as a curl command for use in reports or manual testing',
        parameters: z.object({
            requestId: z.number().describe('request_id from project SQLite DB'),
            compressed: z.boolean().optional().describe('Add --compressed flag'),
            insecure: z.boolean().optional().describe('Add -k/--insecure flag for self-signed certs'),
        }),
        inputExample: { requestId: 42 },
        outputExample: {},
    },

    'template': {
        title: 'Request Templates',
        icon: 'ri:file-copy-line',
        description: 'Manage and execute request templates with variable substitution. Actions: register, send, sendBatch, sendSequence, list, delete',
        parameters: z.object({
            action: z.enum(['register', 'send', 'sendBatch', 'sendSequence', 'list', 'delete']),
            name: z.string().optional().describe('Template name (register, delete)'),
            tls: z.boolean().optional().describe('Use HTTPS (register)'),
            host: z.string().optional().describe('Target hostname (register)'),
            port: z.number().optional().describe('Target port (register)'),
            httpVersion: z.number().optional().describe('HTTP version: 1 or 2 (register)'),
            requestTemplate: z.string().optional().describe('Raw HTTP request with ${VAR} placeholders (register)'),
            variables: z.record(z.string()).optional().describe('Default variable values (register) or overrides (send)'),
            description: z.string().optional().describe('Template description (register)'),
            injectSession: z.boolean().optional().describe('Auto-inject active session cookies/headers (register)'),
            jsonEscapeVars: z.boolean().optional().describe('JSON-escape variable values before substitution (register)'),
            extractRegex: z.string().optional().describe('Regex to extract from response (register)'),
            extractGroup: z.number().optional().describe('Capture group for extraction (register)'),
            templateName: z.string().optional().describe('Template name to use (send, sendBatch)'),
            variableSets: z.array(z.record(z.string())).optional().describe('Array of variable sets (sendBatch)'),
            steps: z.array(z.any()).optional().describe('Ordered steps to execute (sendSequence)'),
            note: z.string().optional().describe('Note to attach to request (send, sendBatch)'),
        }),
        inputExample: { action: 'list' },
        outputExample: {},
    },

    // === Sessions / scope / tagging ===
    'session': {
        title: 'Session Manager',
        icon: 'ri:account-circle-line',
        description: 'Manage sessions, cookies, and CSRF tokens. Actions: create, list, switch, delete, getHeaders, updateCookies, getCookies, setCookie, csrfExtract',
        parameters: z.object({
            action: z.enum(['create', 'list', 'switch', 'delete', 'getHeaders', 'updateCookies', 'getCookies', 'setCookie', 'csrfExtract']),
            name: z.string().optional().describe('Session name'),
            cookies: z.record(z.string()).optional().describe('Initial cookies (create)'),
            headers: z.record(z.string()).optional().describe('Custom headers (create)'),
            cookieValues: z.array(z.string()).optional().describe('Set-Cookie values to merge (updateCookies)'),
            cookieName: z.string().optional().describe('Cookie name (setCookie)'),
            cookieValue: z.string().optional().describe('Cookie value (setCookie)'),
            content: z.string().optional().describe('HTML content to extract CSRF tokens from (csrfExtract)'),
            customPatterns: z.array(z.string()).optional().describe('Custom regex patterns (csrfExtract)'),
            sessionName: z.string().optional().describe('Session to store extracted CSRF token in (csrfExtract)'),
        }),
        inputExample: { action: 'list' },
        outputExample: {},
    },

    'scope': {
        title: 'Scope Rules',
        icon: 'ri:focus-3-line',
        description: 'Manage scope rules for target URL filtering. Actions: load, check, checkMultiple, getRules, addRule, removeRule, reset',
        parameters: z.object({
            action: z.enum(['load', 'check', 'checkMultiple', 'getRules', 'addRule', 'removeRule', 'reset']),
            filePath: z.string().optional().describe('Path to scope.yaml (load)'),
            url: z.string().optional().describe('URL to check (check)'),
            urls: z.array(z.string()).optional().describe('URLs to check (checkMultiple)'),
            ruleType: z.string().optional().describe('include or exclude (addRule, removeRule)'),
            host: z.string().optional().describe('Host pattern (addRule)'),
            protocol: z.string().optional().describe('http, https, or empty (addRule)'),
            port: z.string().optional().describe('Port or empty (addRule)'),
            path: z.string().optional().describe('Path prefix or empty (addRule)'),
            reason: z.string().optional().describe('Reason for rule (addRule)'),
            index: z.number().optional().describe('Rule index to remove (removeRule)'),
            confirm: z.boolean().optional().describe('Must be true to confirm (reset)'),
        }),
        inputExample: { action: 'getRules' },
        outputExample: {},
    },

    'trafficTag': {
        title: 'Traffic Tagger',
        icon: 'ri:price-tag-3-line',
        description: 'Tag and annotate traffic in the project DB. Actions: add, get, list, delete',
        parameters: z.object({
            action: z.enum(['add', 'get', 'list', 'delete']),
            requestId: z.number().optional().describe('request_id from project DB (add, get, delete)'),
            tag: z.string().optional().describe('Tag name (add, get, delete)'),
            note: z.string().optional().describe('Optional note (add)'),
            limit: z.number().optional().describe('Max results (get, default: 100)'),
        }),
        inputExample: { action: 'list' },
        outputExample: {},
    },

    // === Fuzzing / racing / authz ===
    'fuzz': {
        title: 'Fuzzer',
        icon: 'ri:radar-line',
        description: 'Grammar-based request fuzzing with marker substitution. Actions: configure (set target, request template, payloads), start (begin fuzzing), stop (halt), status (progress), results (get findings)',
        parameters: z.object({
            action: z.enum(['configure', 'start', 'stop', 'status', 'results']),
            host: z.string().optional().describe('Target host'),
            port: z.number().optional().describe('Target port (default 80, or 443 with TLS)'),
            tls: z.boolean().optional(),
            http2: z.boolean().optional(),
            request: z.string().optional().describe('Raw HTTP request template with §marker§ placeholders'),
            markers: z.record(z.array(z.string())).optional().describe('Map of marker placeholder to payload list'),
            mode: z.string().optional().describe('cluster_bomb (default, all combinations) or pitch_fork (parallel iteration)'),
            concurrency: z.number().optional().describe('Number of concurrent workers (default 40)'),
            timeout: z.number().optional().describe('Request timeout in seconds (default 10)'),
            limit: z.number().optional().describe('Max results to return for results action (default 100)'),
        }),
        inputExample: { action: 'status' },
        outputExample: {},
    },

    'raceTest': {
        title: 'Race Condition Tester',
        icon: 'ri:flashlight-line',
        description: 'Race condition testing with simultaneous requests. Actions: parallel, parallelDifferent, h2SinglePacket, lastByteSync, firstSequenceSync',
        parameters: z.object({
            action: z.enum(['parallel', 'parallelDifferent', 'h2SinglePacket', 'lastByteSync', 'firstSequenceSync']),
            host: z.string().optional(),
            port: z.number().optional(),
            tls: z.boolean().optional(),
            request: z.string().optional().describe('Raw HTTP request (parallel, lastByteSync)'),
            requests: z.array(z.string()).optional().describe('Different raw HTTP requests (parallelDifferent, h2SinglePacket)'),
            count: z.number().optional().describe('Number of identical requests (parallel, lastByteSync, max 50)'),
            note: z.string().optional(),
        }),
        inputExample: { action: 'parallel', host: 'example.com', port: 443, tls: true, request: 'POST /redeem ...', count: 25 },
        outputExample: {},
    },

    'authzTest': {
        title: 'Authorization Tester',
        icon: 'ri:shield-cross-line',
        description: 'Automated authorization testing. Replay requests with different sessions to find access control issues. Actions: configure, run, results',
        parameters: z.object({
            action: z.enum(['configure', 'run', 'results']),
            highPrivSession: z.string().optional().describe('Name of the high-privilege session (original requests)'),
            lowPrivSession: z.string().optional().describe('Name of the low-privilege session to swap in'),
            unauthenticated: z.boolean().optional().describe('Also test with no cookies/auth headers'),
            host: z.string().optional().describe('Filter traffic by host for run action'),
            limit: z.number().optional().describe('Max requests to test (default 50)'),
        }),
        inputExample: { action: 'configure', highPrivSession: 'admin', lowPrivSession: 'user' },
        outputExample: {},
    },

    'generateWordlist': {
        title: 'Generate Wordlist',
        icon: 'ri:file-list-line',
        description: 'Generate wordlist from discovered paths and/or parameters',
        parameters: z.object({
            source: z.enum(['paths', 'parameters', 'both']),
            hostFilter: z.string().optional().describe('Only extract from this host'),
            outputPath: z.string().describe('File path to write the wordlist to'),
        }),
        inputExample: { source: 'both', outputPath: '/tmp/wordlist.txt' },
        outputExample: {},
    },

    // === Browser (CamoFox) ===
    'browser': {
        title: 'Browser Tabs',
        icon: 'ri:window-line',
        description: 'Browser tab lifecycle and observation. Actions: open, close, list, navigate, back, forward, refresh, screenshot, snapshot',
        parameters: z.object({
            action: z.enum(['open', 'close', 'list', 'navigate', 'back', 'forward', 'refresh', 'screenshot', 'snapshot']),
            tabId: z.string().optional().describe('Tab ID (required for most actions except open and list)'),
            url: z.string().optional().describe('URL to navigate to (open, navigate)'),
            preset: z.string().optional().describe('Geo preset: us-east, us-west, japan, uk, germany, vietnam, singapore, australia (open)'),
            fullPage: z.boolean().optional().describe('Capture full page screenshot (screenshot)'),
        }),
        inputExample: { action: 'open', url: 'https://example.com' },
        outputExample: {},
    },

    'browserInteract': {
        title: 'Browser Interact',
        icon: 'ri:cursor-line',
        description: 'Browser page interaction. Actions: click, type, fill, press, scroll, hover, waitForText, waitForSelector',
        parameters: z.object({
            action: z.enum(['click', 'type', 'fill', 'press', 'scroll', 'hover', 'waitForText', 'waitForSelector']),
            tabId: z.string().describe('Tab ID'),
            ref: z.string().optional().describe('Element ref from snapshot (e.g. e1, e2)'),
            selector: z.string().optional().describe('CSS selector (fallback if no ref)'),
            text: z.string().optional().describe('Text to type (type, fill)'),
            key: z.string().optional().describe('Key to press (press) e.g. Enter, Tab, Escape'),
            direction: z.enum(['up', 'down']).optional().describe('Scroll direction (scroll)'),
            amount: z.number().optional().describe('Scroll amount in pixels (scroll, default 300)'),
            timeout: z.number().optional().describe('Wait timeout in ms (waitForText, waitForSelector, default 10000)'),
        }),
        inputExample: { action: 'click', tabId: 'tab-1', ref: 'e1' },
        outputExample: {},
    },

    'browserExec': {
        title: 'Browser Exec',
        icon: 'ri:code-box-line',
        description: 'Execute JS and access browser data. Actions: evaluate, getHtml, getLinks, getCookies, setCookies, getConsole, getErrors',
        parameters: z.object({
            action: z.enum(['evaluate', 'getHtml', 'getLinks', 'getCookies', 'setCookies', 'getConsole', 'getErrors']),
            tabId: z.string().describe('Tab ID'),
            expression: z.string().optional().describe('JavaScript expression (evaluate)'),
            timeout: z.number().optional().describe('Execution timeout in ms (evaluate, default 30000)'),
            cookies: z.array(z.record(z.any())).optional().describe('Cookies to set (setCookies)'),
        }),
        inputExample: { action: 'evaluate', tabId: 'tab-1', expression: 'document.title' },
        outputExample: {},
    },

    'browserSec': {
        title: 'Browser Security',
        icon: 'ri:shield-keyhole-line',
        description: 'Browser security/admin (CamoFox): auth + XSS testing + server config in one tool. Actions: login|importCookies|exportCookies (auth), verifyAlert|injectPayload|testDomSink|checkCsp|disableCsp|disableCors|disableFrameProtection|restoreDefaults (xss), status|setDisplay|setCamofoxUrl (config). For day-to-day page driving use browser/browserInteract/browserExec instead.',
        parameters: z.object({
            action: z.enum([
                'login', 'importCookies', 'exportCookies',
                'verifyAlert', 'injectPayload', 'testDomSink', 'checkCsp',
                'disableCsp', 'disableCors', 'disableFrameProtection', 'restoreDefaults',
                'status', 'setDisplay', 'setCamofoxUrl',
            ]),
            tabId: z.string().optional().describe('Tab ID (most actions)'),
            url: z.string().optional().describe('URL (login, verifyAlert, testDomSink)'),
            // auth
            username: z.string().optional().describe('Username (login)'),
            password: z.string().optional().describe('Password (login)'),
            usernameRef: z.string().optional().describe('Ref or selector for username field (login)'),
            passwordRef: z.string().optional().describe('Ref or selector for password field (login)'),
            submitRef: z.string().optional().describe('Ref or selector for submit button (login)'),
            cookies: z.array(z.record(z.any())).optional().describe('Cookies to import [{name,value,domain,path}] (importCookies)'),
            // xss
            payload: z.string().optional().describe('XSS payload HTML/JS (injectPayload)'),
            selector: z.string().optional().describe('Target element selector (injectPayload)'),
            sink: z.string().optional().describe('DOM sink to test: location.hash, document.referrer, window.name, postMessage (testDomSink)'),
            value: z.string().optional().describe('Value to inject into sink (testDomSink)'),
            // config
            headless: z.string().optional().describe('Display mode: true (headless), false (headed), virtual (VNC) (setDisplay)'),
            camofoxUrl: z.string().optional().describe('CamoFox server URL e.g. http://localhost:9377 (setCamofoxUrl)'),
        }),
        inputExample: { action: 'status' },
        outputExample: {},
    },

    // === Crypto / encoding ===
    'jwt': {
        title: 'JWT Tooling',
        icon: 'ri:key-2-line',
        description: 'JWT security testing. Actions: decode, forge, noneAttack, keyConfusion, bruteforce',
        parameters: z.object({
            action: z.enum(['decode', 'forge', 'noneAttack', 'keyConfusion', 'bruteforce']),
            token: z.string().optional().describe('JWT token (decode, noneAttack, keyConfusion, bruteforce)'),
            header: z.string().optional().describe('JWT header JSON (forge)'),
            payload: z.string().optional().describe('JWT payload JSON (forge)'),
            secret: z.string().optional().describe('HMAC secret for HS256/384/512 (forge)'),
            privateKey: z.string().optional().describe('RSA private key PEM (forge)'),
            publicKey: z.string().optional().describe('RSA public key PEM (keyConfusion)'),
            wordlist: z.array(z.string()).optional().describe('Custom secrets to try (bruteforce)'),
        }),
        inputExample: { action: 'decode', token: 'eyJ...' },
        outputExample: {},
    },

    'encode': {
        title: 'Encoder',
        icon: 'ri:lock-2-line',
        description: 'Encode/decode strings or generate random values. Actions: urlEncode, urlDecode, b64Encode, b64Decode, random',
        parameters: z.object({
            action: z.enum(['urlEncode', 'urlDecode', 'b64Encode', 'b64Decode', 'random']),
            content: z.string().optional().describe('Input string (urlEncode, urlDecode, b64Encode, b64Decode)'),
            length: z.number().optional().describe('Length of random string (random)'),
            charset: z.string().optional().describe('Character set for random generation (random, default: alphanumeric)'),
        }),
        inputExample: { action: 'b64Encode', content: 'hello' },
        outputExample: {},
    },

    // === Network / recon ===
    'ja4': {
        title: 'JA4 Fingerprint',
        icon: 'ri:fingerprint-line',
        description: 'JA4+ TLS fingerprint lookup from proxy traffic. Actions: lookup (by host), list (all cached)',
        parameters: z.object({
            action: z.enum(['lookup', 'list']),
            host: z.string().optional().describe('Hostname to look up'),
        }),
        inputExample: { action: 'lookup', host: 'example.com' },
        outputExample: {},
    },

    'oob': {
        title: 'OOB Server',
        icon: 'ri:radar-line',
        description: 'Out-of-band callback server for blind vulnerability detection. Actions: start, stop, generatePayload, pollInteractions, clearInteractions',
        parameters: z.object({
            action: z.enum(['start', 'stop', 'connectRemote', 'generatePayload', 'pollInteractions', 'clearInteractions']),
            host: z.string().optional().describe('External hostname/IP for payloads (e.g. your-vps.com)'),
            httpPort: z.number().optional().describe('HTTP listener port (default 9999)'),
            dnsPort: z.number().optional().describe('DNS listener port (default 0 disabled, set >0 to enable)'),
            dnsResponseIP: z.string().optional().describe('IP to respond with for DNS A queries (default 127.0.0.1)'),
            token: z.string().optional().describe('Filter interactions by token'),
            remoteUrl: z.string().optional().describe('URL of remote lorg-oob server'),
            remoteToken: z.string().optional().describe('API bearer token for remote lorg-oob server'),
        }),
        inputExample: { action: 'start' },
        outputExample: {},
    },

    'graphql': {
        title: 'GraphQL Tester',
        icon: 'ri:share-line',
        description: 'GraphQL security testing. Actions: introspect (with bypass techniques), buildQuery, suggestPayloads',
        parameters: z.object({
            action: z.enum(['introspect', 'buildQuery', 'suggestPayloads']),
            host: z.string().optional().describe('Target hostname (introspect)'),
            port: z.number().optional().describe('Target port (introspect)'),
            tls: z.boolean().optional().describe('Use HTTPS (introspect)'),
            path: z.string().optional().describe('GraphQL endpoint path (introspect, default: /graphql)'),
            headers: z.record(z.string()).optional().describe('Additional headers (introspect)'),
            bypassTechnique: z.string().optional().describe('Bypass: none, get, newline, whitespace, aliased, fragments (introspect)'),
            schema: z.string().optional().describe('Schema JSON from introspect (buildQuery)'),
            typeName: z.string().optional().describe('Type name to build query for (buildQuery)'),
            maxDepth: z.number().optional().describe('Maximum nesting depth (buildQuery, default: 2)'),
            category: z.string().optional().describe('Attack category: injection, auth_bypass, info_disclosure, dos, batching, all (suggestPayloads)'),
        }),
        inputExample: { action: 'introspect', host: 'api.example.com', tls: true },
        outputExample: {},
    },

    'openapi': {
        title: 'OpenAPI Tools',
        icon: 'ri:book-2-line',
        description: 'OpenAPI/Swagger spec import and request generation. Actions: import (parse JSON spec), listEndpoints (show routes), generateRequests (build raw HTTP requests)',
        parameters: z.object({
            action: z.enum(['import', 'listEndpoints', 'generateRequests']),
            spec: z.string().optional().describe('OpenAPI 3.x JSON spec string (for import)'),
            host: z.string().optional().describe('Target host for request generation (overrides spec servers)'),
            path: z.string().optional().describe('Filter by specific path prefix'),
            method: z.string().optional().describe('Filter by HTTP method'),
        }),
        inputExample: { action: 'listEndpoints' },
        outputExample: {},
    },

    // === Data / wire formats ===
    'protobuf': {
        title: 'Protobuf Decoder',
        icon: 'ri:braces-line',
        description: 'Decode protobuf wire format without .proto files. Actions: decode (base64), decodeHex (hex), decodeTraffic (decode gRPC response by request ID)',
        parameters: z.object({
            action: z.enum(['decode', 'decodeHex', 'decodeTraffic']),
            data: z.string().optional().describe('Base64 or hex-encoded protobuf data'),
            requestId: z.string().optional().describe('Request ID for decodeTraffic action'),
        }),
        inputExample: { action: 'decode', data: 'CgVoZWxsbw==' },
        outputExample: {},
    },

    'websocket': {
        title: 'WebSocket Inspector',
        icon: 'ri:plug-line',
        description: 'WebSocket traffic analysis. Actions: listMessages (by host/connection), search (content search), getConnection (upgrade details), listConnections (all WS connections)',
        parameters: z.object({
            action: z.enum(['listMessages', 'search', 'getConnection', 'listConnections']),
            host: z.string().optional().describe('Filter by host'),
            requestId: z.string().optional().describe('Filter by proxy request ID (connection identifier)'),
            query: z.string().optional().describe('Search string for message payloads'),
            direction: z.enum(['send', 'recv']).optional().describe('Filter by direction'),
            limit: z.number().optional().describe('Max results (default 100)'),
        }),
        inputExample: { action: 'listConnections' },
        outputExample: {},
    },

    'sseClient': {
        title: 'SSE Client',
        icon: 'ri:rss-line',
        description: 'SSE (Server-Sent Events) testing client. Actions: connect (open stream), listEvents (get captured events), disconnect (close), listConnections (show all)',
        parameters: z.object({
            action: z.enum(['connect', 'listEvents', 'disconnect', 'listConnections']),
            url: z.string().optional().describe('SSE endpoint URL (required for connect/listEvents/disconnect)'),
            headers: z.record(z.string()).optional().describe('Custom headers for the SSE connection'),
            limit: z.number().optional().describe('Max events to return (default 100)'),
        }),
        inputExample: { action: 'connect', url: 'https://example.com/events' },
        outputExample: {},
    },

};
