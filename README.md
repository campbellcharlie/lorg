# lorg

**AI-powered penetration testing proxy** — 60 MCP tools, browser integration, and per-project SQLite databases.

*lorg* (Gaelic: track, trace, trail) is a security testing toolkit that combines an intercepting HTTP/HTTPS proxy with a comprehensive MCP (Model Context Protocol) server. Designed to be driven by AI agents like Claude Code, it provides everything needed for web application security assessments without requiring a Burp Suite license.

## Features

- **Intercepting Proxy** — HTTP/1.1, HTTP/2, WebSocket with TLS fingerprint mimicry (uTLS)
- **60 MCP Tools** — request sending, session management, JWT attacks, race conditions, GraphQL testing, scope enforcement, response analysis, and more
- **Browser Integration** — CamoFox (anti-fingerprint Firefox) control via 6 pentest-focused tools including XSS verification and CSP bypass
- **Per-Project SQLite DB** — all traffic logged in real-time to burp-mcp-enhanced compatible databases
- **Minimal Web UI** — traffic viewer, repeater, syntax highlighting, 10 color themes, resizable panes
- **Session Management** — multiple named sessions with cookie jars, CSRF auto-capture/injection
- **No License Required** — fully open source, runs headless or with UI

## Quick Start

```bash
# Build
go build -o lorg ./cmd/lorg/

# Run (auto-starts proxy on :9090, API/UI on :8090)
./lorg serve --http 127.0.0.1:8090

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
| **Request Sending** | `sendHttpRequest`, `sendRequest`, `replayFromDb` | Structured HTTP with session inject, CSRF, redirects, regex extraction |
| **Browser** | `browser`, `browserInteract`, `browserExec`, `browserXss`, `browserAuth`, `browserConfig` | CamoFox control, XSS verification, CSP/CORS bypass |
| **Session** | `session` | create, list, switch, delete, getHeaders, updateCookies, getCookies, setCookie, csrfExtract |
| **JWT** | `jwt` | decode, forge, noneAttack, keyConfusion, bruteforce |
| **Scope** | `scope` | load (YAML), check, checkMultiple, getRules, addRule, removeRule, reset |
| **Templates** | `template` | register, send, sendBatch, sendSequence, list, delete |
| **Race Testing** | `raceTest` | parallel, parallelDifferent, h2SinglePacket, lastByteSync |
| **Traffic** | `searchTraffic`, `searchTrafficRegex`, `getTrafficStats`, `getEndpoints`, `getParameters`, `generateWordlist` | Search, analyze, extract |
| **Tags** | `trafficTag` | add, get, list, delete |
| **Response Analysis** | `extract`, `analyze`, `compare` | regex/jsonPath/between, response/variations/keywords, responses/byId |
| **GraphQL** | `graphql` | introspect (with bypass techniques), buildQuery, suggestPayloads |
| **Project** | `project` | setup, info, setName, export, setLogging |
| **Encoding** | `encode` | urlEncode, urlDecode, b64Encode, b64Decode, random |
| **Raw Socket** | `sendRaw` | tcp, tls, h2Sequence |

## Architecture

```
Claude Code --> lorg MCP (port 8090) --> CamoFox (port 9377) --> Firefox
                       |
                       |-- lorg proxy (port 9090) <-- all HTTP traffic
                       |
                       |-- PocketBase (SQLite) <-- traffic DB
                       |
                       '-- Project SQLite DB <-- burp-mcp-enhanced compatible
```

## UI Themes

10 built-in themes: Obsidian (default), Dracula, Gruvbox, Nord, Monokai Pro, Solarized Dark, Catppuccin Mocha, Rose Pine, Midnight Blue, Ember.

## Requirements

- Go 1.24+
- CamoFox (optional, for browser testing)

## Based On

Originally forked from [grroxy](https://github.com/glitchedgitz/grroxy) by glitchedgitz.
