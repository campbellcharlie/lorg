<div align="center">

# ⇝ lorg

**An AI-driven web-application pentest proxy — an intercepting HTTP/HTTPS proxy fused with a comprehensive MCP server, built to be driven by an AI agent.**

![mission](https://img.shields.io/badge/mission-AI--driven_pentest_proxy-7d3cff)
![license](https://img.shields.io/badge/license-MIT-green)
![lang](https://img.shields.io/badge/Go-1.25+-black)
![exposes](https://img.shields.io/badge/exposes-MCP_server-orange)
![driven by](https://img.shields.io/badge/driven_by-Claude_Code-blue)

[What & Why](#what--why) · [Philosophy](#philosophy) · [Measurement](#measurement) · [Quickstart](#quickstart) · [Tool Categories](#tool-categories) · [Architecture](#architecture)

</div>

---

## What & Why

An AI agent testing a web app needs what a human pentester has: an intercepting proxy that sees and rewrites every request, a toolbox for tampering, smuggling, JWT/GraphQL/race work, and browser-based verification — and a capture store it can actually read. Most such tooling is GUI-first, hard to drive programmatically, and keeps its data in an **opaque store an agent can't query**.

**lorg** (Gaelic: *track, trace, trail*) is that surface, built for an agent instead of a mouse: an intercepting HTTP/1.1/2 + WebSocket proxy (with uTLS fingerprint mimicry), a **comprehensive MCP server** exposing ~40+ tools, an anti-detect browser (CamoFox) for XSS/DOM verification, and **per-project SQLite** — a plain, agent-readable DB it can query directly, one portable, isolated file per engagement. Point Claude Code at it and it has everything for a web assessment — headless or with a UI.

## Philosophy

> **The agent drives the proxy; the proxy is just the hands.**

An LLM can reason about traffic, but it can't *see* or *rewrite* it on its own. lorg is the deterministic surface underneath — capture, tamper, replay, verify — exposed as tools the model calls, with the truth living in the traffic DB rather than the model's memory.

> One engagement, one file.

Per-project SQLite means each engagement is self-contained and portable: sends address any project per call, reads union across projects, and nothing leaks between clients. Isolation is the default, not a discipline you have to remember.

## Measurement

What is **verified**: the MCP surface is self-describing and test-covered — `/mcp/health` lists the live tool surface, and the Go suite pins tool counts and gates the sharp edges (JA4 ClientHello fingerprinting, **fail-closed** outbound scope enforcement, raw/HTTP2 framing) with real tests (`go test ./...`).

What is **a hypothesis**: that agent-driven testing over this surface finds *more* than a human running the same tooling by hand. That's per-engagement and unproven as a blanket claim — treat any "finds more / faster" statement as a conjecture until a benchmark cites a number.

## Quickstart

Requires **Go 1.25+** (CamoFox optional, for browser testing).

```bash
git clone https://github.com/campbellcharlie/lorg.git ~/src/lorg && cd ~/src/lorg
go build -o lorg ./cmd/lorg/

# API/UI on 127.0.0.1:8090, MITM proxy auto-started:
./lorg -host 127.0.0.1:8090 -path ~/.lorg/default
open http://127.0.0.1:8090
```

Flags: `-projects-dir <dir>` (per-project DBs, default `~/.lorg/projects`) · `-mcp-token <token>` (bearer token for `/mcp`) · `-log` (verbose).

Wire it into your agent — add to `.mcp.json`:

```json
{
  "mcpServers": {
    "lorg": { "type": "sse", "url": "http://127.0.0.1:8090/mcp/sse" }
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
| **Session** | `session` | create, list, switch, delete, getHeaders, updateCookies, getCookies, setCookie, csrfExtract, copyCookies — per-project cookie jars; `copyCookies` moves selected cookies between project jars |
| **JWT** | `jwt` | decode, forge, noneAttack, keyConfusion, bruteforce |
| **Scope** | `scope` | load (YAML), check, checkMultiple, getRules, addRule, removeRule, reset |
| **Templates** | `template` | register, send, sendBatch, sendSequence, list, delete |
| **Fuzzing** | `fuzz`, `raceTest`, `authzTest` | Marker fuzzing; race conditions (parallel, h2SinglePacket, lastByteSync); session-swap authz testing |
| **Traffic** | `searchTraffic`, `query`, `gatherContext`, `mapEndpoints`, `clusterResponses`, `findAnomalies`, `probeAuth`, `generateWordlist` | Search, query (HTTPQL), per-host stats, endpoint mapping, response clustering, anomaly + auth-boundary surfacing |
| **Tags** | `trafficTag` | add, get, list, delete |
| **Response Analysis** | `responseAnalysis` | analyzeResponse/Variations/Keywords, extractRegex/JsonPath/Between, diffResponses/ById/Structural/Json |
| **GraphQL** | `graphql` | introspect (with bypass techniques), buildQuery, suggestPayloads |
| **OpenAPI** | `openapi` | import, listEndpoints, generateRequests |
| **Project** | `project` | full lifecycle: list, register, setActive, useProject, archive, delete, export, setRedactionMode, … — `useProject` sets a sticky per-connection default |
| **Proxy** | `proxyList`, `proxyStart`, `proxyStop`, `matchReplace` | Lifecycle + match/replace rules |
| **Encoding** | `encode` | urlEncode/Decode, b64Encode/Decode, random |
| **Wire / Streams** | `protobuf`, `websocket`, `sseClient`, `ja4`, `oob` | Protobuf decoding, WebSocket inspection, SSE client, JA4 fingerprints, OOB callback server |
| **Data** | `getRequestResponseFromID`, `lorgStatus` | Fetch raw req/resp by ID, runtime status |

*The live surface is authoritative — `GET /mcp/health` lists exactly what's mounted.*

## Architecture

```
Claude Code --> lorg MCP (port 8090) --> CamoFox (port 9377) --> Firefox
                       |
                       |-- lorg proxy (port 9090) <-- all HTTP traffic
                       |
                       '-- Per-Project SQLite DBs <-- one file per project
                              (http_traffic + http_messages tables)
```

All captured traffic lives in the per-project SQLite stores — each project (engagement) is one portable, independently-archivable file. Sends can be addressed to any project per call; reads (search / query / cluster / mirror / authz) union across project DBs. The config DB holds proxies, scope, match/replace, templates, and project metadata.

<details>
<summary><strong>Screenshots</strong></summary>

**Traffic viewer** — live capture table with the selected request/response below; reads union across every project DB while writes stay addressed to the active project.
![Traffic viewer](screenshots/traffic-viewer.png)

**Request / response viewer** — inspect any captured request and its response with full syntax highlighting.
![Request and response viewer](screenshots/request-response-viewer.png)

**Settings** — 12 themes, editor preferences, scope rules, request templates; the banner shows per-project addressing (one project open read-only while sends land in the active project).
![Settings, themes, and scope rules](screenshots/settings-themes.png)
</details>

<details>
<summary><strong>UI themes</strong></summary>

12 built-in: Obsidian (default), Dracula, Gruvbox, Nord, Monokai Pro, Solarized Dark, Catppuccin Mocha, Rosé Pine, Midnight Blue, Ember, Ayu Mirage Plus, Ayu Light.
</details>

## License

[MIT](LICENSE) © 2023 Gitesh Sharma. lorg started from [grroxy](https://github.com/glitchedgitz/grroxy) and has since diverged substantially.
