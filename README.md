# lorg

**AI-powered penetration testing proxy** — comprehensive MCP tooling, anti-detect browser integration, and per-project SQLite databases.

*lorg* (Gaelic: track, trace, trail) is a security testing toolkit that combines an intercepting HTTP/HTTPS proxy with a comprehensive MCP (Model Context Protocol) server. Designed to be driven by AI agents like Claude Code, it provides everything needed for web application security assessments without requiring a Burp Suite license.

## Features

- **Intercepting Proxy** — HTTP/1.1, HTTP/2, WebSocket with TLS fingerprint mimicry (uTLS)
- **MCP Tooling** — request sending, session management, JWT attacks, race conditions, GraphQL testing, scope enforcement, response analysis, and more (`/mcp/health` lists the live surface)
- **Browser Integration** — CamoFox (anti-fingerprint Firefox) driven by `browser` / `browserInteract` / `browserExec` / `browserSec` for XSS verification, CSP bypass, and DOM-sink testing
- **Per-Project SQLite DB** — all traffic logged in real-time to burp-mcp-enhanced compatible databases
- **Minimal Web UI** — traffic viewer, repeater, syntax highlighting, multiple color themes, resizable panes
- **Session Management** — multiple named sessions with cookie jars, CSRF auto-capture/injection
- **No License Required** — fully open source, runs headless or with UI

## Quick Start

```bash
# Build
go build -o lorg ./cmd/lorg/

# Run — API/UI on 127.0.0.1:8090, MITM proxy auto-started
./lorg -host 127.0.0.1:8090 -path ~/.lorg/default

# Optional flags:
#   -projects-dir <dir>   per-project SQLite DBs (default: ~/.lorg/projects)
#   -mcp-token   <token>  bearer token for the /mcp endpoint
#   -log                  verbose logging

# Open UI
open http://127.0.0.1:8090
```

## MCP Integration

Add to your `.mcp.json`:

```json
{
  "mcpServers": {
    "lorg": {
      "type": "sse",
      "url": "http://127.0.0.1:8090/mcp/sse"
    }
  }
}
```

## Tool Categories

| Category | Tools | Actions |
|---|---|---|
| **Request Sending** | `sendHttpRequest`, `mirror`, `sendRaw`, `exportCurl` | Structured HTTP with session inject + CSRF + redirects + regex extract; `mirror` re-fires a captured row with small mutations (~10× cheaper); `sendRaw` for raw TCP/TLS bytes or HTTP/2 sequences (request smuggling, malformed framing) |
| **Browser** | `browser`, `browserInteract`, `browserExec`, `browserSec` | CamoFox tabs, page interaction, JS exec, security/admin (auth + XSS + config) |
| **Intercept** | `intercept` | toggle, list, getRaw, forward, drop |
| **Hosts** | `host` | list, info, sitemap, rows, getNote, setNote, modifyLabels, modifyNotes |
| **Session** | `session` | create, list, switch, delete, getHeaders, updateCookies, getCookies, setCookie, csrfExtract |
| **JWT** | `jwt` | decode, forge, noneAttack, keyConfusion, bruteforce |
| **Scope** | `scope` | load (YAML), check, checkMultiple, getRules, addRule, removeRule, reset |
| **Templates** | `template` | register, send, sendBatch, sendSequence, list, delete |
| **Fuzzing** | `fuzz`, `raceTest`, `authzTest` | Marker fuzzing; race conditions (parallel, h2SinglePacket, lastByteSync); session-swap authz testing |
| **Traffic** | `searchTraffic`, `query`, `gatherContext`, `mapEndpoints`, `clusterResponses`, `findAnomalies`, `probeAuth`, `generateWordlist` | Search, query (HTTPQL), per-host stats, endpoint mapping, response clustering, anomaly + auth boundary surfacing |
| **Tags** | `trafficTag` | add, get, list, delete |
| **Response Analysis** | `responseAnalysis` | analyzeResponse/Variations/Keywords, extractRegex/JsonPath/Between, diffResponses/ById/Structural/Json |
| **GraphQL** | `graphql` | introspect (with bypass techniques), buildQuery, suggestPayloads |
| **OpenAPI** | `openapi` | import, listEndpoints, generateRequests |
| **Project** | `project` | setup, info, setName, export, setLogging, setRedactionMode, getRedactionMode |
| **Proxy** | `proxyList`, `proxyStart`, `proxyStop`, `matchReplace` | Lifecycle + match/replace rules |
| **Encoding** | `encode` | urlEncode, urlDecode, b64Encode, b64Decode, random |
| **Wire / Streams** | `protobuf`, `websocket`, `sseClient`, `ja4`, `oob` | Protobuf decoding, WebSocket inspection, SSE client, JA4 fingerprints, OOB callback server |
| **Data** | `getRequestResponseFromID`, `lorgStatus` | Fetch raw req/resp by ID, runtime status |

## Architecture

```
Claude Code --> lorg MCP (port 8090) --> CamoFox (port 9377) --> Firefox
                       |
                       |-- lorg proxy (port 9090) <-- all HTTP traffic
                       |
                       |-- lorgdb (SQLite) <-- per-tab traffic DB
                       |
                       '-- Project SQLite DB <-- burp-mcp-enhanced compatible
```

## UI Themes

12 built-in themes: Obsidian (default), Dracula, Gruvbox, Nord, Monokai Pro, Solarized Dark, Catppuccin Mocha, Rosé Pine, Midnight Blue, Ember, Ayu Mirage Plus, Ayu Light.

## Requirements

- Go 1.24+
- CamoFox (optional, for browser testing)

## Based On

Originally forked from [grroxy](https://github.com/glitchedgitz/grroxy) by glitchedgitz.
