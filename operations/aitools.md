# AI Tools - Implementation Tracker

| # | State | Tool Key | Title | Category | Ease | Reason |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | ✅ | `lorgStatus` | lorg Status | System | Very Easy | No params, returns release/backend versions and active state |
| 2 | ✅ | `project` | Project Manager | Project | Medium | Manage project settings, SQLite DB, traffic logging, redaction. Actions: setup, info, setName, export, setLogging, setRedactionMode, getRedactionMode |
| 3 | ✅ | `proxyList` | List Proxies | Proxy | Very Easy | Returns running proxy instances with status, browser type, configuration |
| 4 | ✅ | `proxyStart` | Start Proxy | Proxy | Easy | Start a new proxy instance, optionally attach a browser (firefox/none) |
| 5 | ✅ | `proxyStop` | Stop Proxy | Proxy | Easy | Stop one proxy by ID, or stop all when ID omitted |
| 6 | ✅ | `intercept` | Intercept Manager | Intercept | Medium | Single dispatcher for proxy interception. Actions: toggle, list, getRaw, forward, drop |
| 7 | ✅ | `matchReplace` | Match & Replace | Proxy | Medium | Manage rules that auto-modify requests/responses inline. Actions: add, list, remove, enable, disable, reload |
| 8 | ✅ | `host` | Host Manager | Target | Medium | Host inventory + per-host details. Actions: list, info, sitemap, rows, getNote, setNote, modifyLabels, modifyNotes |
| 9 | ✅ | `searchTraffic` | Search Traffic | Search | Medium | Filter captured traffic by host/path/method/status or substring/regex on raw req/resp. regex=true switches to Go regex mode (regexSource: request/response/both) |
| 10 | ✅ | `query` | HTTPQL Query | Search | Medium | HTTPQL-like query language (req.host.cont:..., resp.status.eq:...). Actions: search, explain |
| 11 | ✅ | `getRequestResponseFromID` | Get Request/Response | Data | Very Easy | Fetch raw request/response bytes for a captured row by activeID |
| 12 | ✅ | `gatherContext` | Gather Context | Recon | Medium | Structured intelligence from project DB: endpoints, parameters, status distribution, MIME types, error signatures. Replaces getTrafficStats/getStatusDistribution/getEndpoints/getParameters |
| 13 | ✅ | `mapEndpoints` | Map Endpoints | Recon | Medium | Distinct method+pathTemplate tuples per host, hit counts, status distribution, fingerprint counts. Replaces getEndpoints + status-distribution sequence |
| 14 | ✅ | `clusterResponses` | Cluster Responses | Analysis | Medium | Group captured responses by structural fingerprint (status+mime+shape+length bucket) |
| 15 | ✅ | `findAnomalies` | Find Anomalies | Analysis | Medium | List responses whose fingerprint differs from the modal one for a host/endpoint |
| 16 | ✅ | `responseAnalysis` | Response Analysis | Analysis | Hard | Inspect, extract, or diff HTTP responses. Replaces extract/analyze/compare. Actions: analyzeResponse/Variations/Keywords, extractRegex/JsonPath/Between, diffResponses/ById/Structural/Json |
| 17 | ✅ | `probeAuth` | Probe Auth Boundary | Recon | Medium | Surface auth boundary: credentialed endpoints, 401/403 buckets, ready-to-replay probe candidates. Read-only |
| 18 | ✅ | `sendHttpRequest` | Send HTTP Request | Requests | Medium | Primary structured request tool: method/url/headers/body, session injection, CSRF, redirects, regex extraction |
| 19 | ✅ | `sendRaw` | Send Raw Bytes | Requests | Hard | Raw TCP/TLS bytes or HTTP/2 sequences. Actions: tcp, tls, h2Sequence |
| 20 | ✅ | `mirror` | Mirror Request | Requests | Medium | Re-fire a captured row (or saved template) with small mutations. Cheap re-probe primitive (~10x token saving over rebuilding sendHttpRequest) |
| 21 | ✅ | `exportCurl` | Export as curl | Requests | Very Easy | Export a stored request as a curl command for reports or manual testing |
| 22 | ✅ | `template` | Request Templates | Requests | Hard | Manage and execute request templates with variable substitution. Actions: register, send, sendBatch, sendSequence, list, delete |
| 23 | ✅ | `session` | Session Manager | Project | Medium | Sessions, cookies, CSRF tokens. Actions: create, list, switch, delete, getHeaders, updateCookies, getCookies, setCookie, csrfExtract |
| 24 | ✅ | `scope` | Scope Rules | Project | Medium | Target URL filtering. Actions: load, check, checkMultiple, getRules, addRule, removeRule, reset |
| 25 | ✅ | `trafficTag` | Traffic Tagger | Project | Easy | Tag/annotate traffic in the project DB. Actions: add, get, list, delete |
| 26 | ✅ | `fuzz` | Fuzzer | Fuzzer | Hard | Grammar-based request fuzzing with marker substitution. Actions: configure, start, stop, status, results |
| 27 | ✅ | `raceTest` | Race Condition Tester | Fuzzer | Hard | Simultaneous requests to surface race conditions. Actions: parallel, parallelDifferent, h2SinglePacket, lastByteSync, firstSequenceSync |
| 28 | ✅ | `authzTest` | Authorization Tester | Fuzzer | Hard | Replay traffic with swapped sessions to find access-control issues. Actions: configure, run, results |
| 29 | ✅ | `generateWordlist` | Generate Wordlist | Recon | Easy | Generate a wordlist from discovered paths and/or parameters |
| 30 | ✅ | `browser` | Browser Tabs | Browser | Medium | CamoFox tab lifecycle + observation. Actions: open, close, list, navigate, back, forward, refresh, screenshot, snapshot |
| 31 | ✅ | `browserInteract` | Browser Interact | Browser | Medium | Page interaction. Actions: click, type, fill, press, scroll, hover, waitForText, waitForSelector |
| 32 | ✅ | `browserExec` | Browser Exec | Browser | Medium | JS execution + browser data access. Actions: evaluate, getHtml, getLinks, getCookies, setCookies, getConsole, getErrors |
| 33 | ✅ | `browserSec` | Browser Security | Browser | Hard | CamoFox auth + XSS testing + server config. Replaces browserAuth/browserXss/browserConfig. Actions: login, importCookies, exportCookies, verifyAlert, injectPayload, testDomSink, checkCsp, disableCsp, disableCors, disableFrameProtection, restoreDefaults, status, setDisplay, setCamofoxUrl |
| 34 | ✅ | `jwt` | JWT Tooling | Crypto | Hard | JWT security testing. Actions: decode, forge, noneAttack, keyConfusion, bruteforce |
| 35 | ✅ | `encode` | Encoder | Crypto | Very Easy | Encode/decode or generate random. Actions: urlEncode, urlDecode, b64Encode, b64Decode, random |
| 36 | ✅ | `ja4` | JA4 Fingerprint | Recon | Easy | JA4+ TLS fingerprint lookup from proxy traffic. Actions: lookup, list |
| 37 | ✅ | `oob` | OOB Server | Recon | Hard | Out-of-band callback server for blind vuln detection. Actions: start, stop, generatePayload, pollInteractions, clearInteractions |
| 38 | ✅ | `graphql` | GraphQL Tester | Recon | Hard | GraphQL security testing. Actions: introspect (with bypass techniques), buildQuery, suggestPayloads |
| 39 | ✅ | `openapi` | OpenAPI Tools | Recon | Medium | OpenAPI/Swagger import + request generation. Actions: import, listEndpoints, generateRequests |
| 40 | ✅ | `protobuf` | Protobuf Decoder | Data | Medium | Decode protobuf wire format without .proto files. Actions: decode (b64), decodeHex, decodeTraffic |
| 41 | ✅ | `websocket` | WebSocket Inspector | Data | Medium | WebSocket traffic analysis. Actions: listMessages, search, getConnection, listConnections |
| 42 | ✅ | `sseClient` | SSE Client | Data | Medium | Server-Sent Events client. Actions: connect, listEvents, disconnect, listConnections |
