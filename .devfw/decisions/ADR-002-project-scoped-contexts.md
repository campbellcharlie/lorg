---
artifact: decision
version: 1
status: accepted
owners: [campbellcharlie]
last_updated: 2026-06-13
---

# ADR-002 Project-scoped contexts: capture-binding + send-addressing + per-project session jars

## Context

ADR-001 made ServalSync traffic project-queryable and aligned the Active project
DB on startup, but it kept — and explicitly accepted as Won't-Fix — lorg's
**single global Active write-target**. That global is the root of the operator's
real pain:

> "Sometimes I have 3–4 projects open and within minutes need to swap between
> them to send requests, while a browser or Serval is still capturing into one
> project."

Today there is exactly one write target and switching it is a server-wide
mutation:

- `ProjectDB.LogTraffic` writes every captured row to the single active handle
  `p.db` (`apps/app/mcp_project.go:351,441,475,482`); it does **not** route by
  the traffic's project. The per-`ProxyInstance` `.Project` field
  (`apps/app/proxy.go:24`) and `_data.project` tag are row labels, not routing
  keys.
- The only way to redirect writes is `projectDB.SetProject` via
  `POST /api/project/setActive` (`apps/app/project_rest.go:266`), which closes
  the old handle and opens a new one **for the whole process**.
- Sessions/cookie jars are global and flat: `_sessions` has a
  `UNIQUE INDEX (name)` (`internal/lorgdb/migrate.go:494`), a single global
  `active = ?` row (`apps/app/mcp_sessions.go:78`), and **no project column**.

So "send a few requests to project B while a browser captures into A" has no
clean path. The naive `setActive(B) → send → setActive(A)` is **racy**: A's
in-flight capture lands in B during the window. And even if writes routed
correctly, a B-addressed send would still inject A's globally-active cookie jar.

### Industry grounding

Caido (the closest comparison) solves the human side with **per-project
isolation + instant switch, one active capture target at a time** — verified
from its data-storage docs (per-UUID SQLite per project; live switch, no
restart). No mainstream proxy (Burp, ZAP, Caido) does concurrent multi-project
capture or agent-addressed per-call sends; those are lorg-native, MCP-driven
capabilities and carry no prior art (higher risk, the differentiator).

## Decision

Stop treating "the active project" as a write **router**. Make **project a
complete, addressable context** — `{ write DB handle, session/cookie jar,
scope, match/replace }` — that is **bound on capture sources** and **addressed
per call on sends**. The global Active survives only as a *default* for callers
that name no project.

Four parts, delivered as vertical slices (see `plan.md`):

1. **Capture-binding + write routing (Slice 1).** Add a per-project open
   **write-handle registry** to `ProjectDB` (the write-side twin of the existing
   read-only `readDBCache`). Route `LogTraffic` by `userdata.Project` instead of
   the single `p.db`; empty project ⇒ the Active handle (unchanged default).
   Carry the project on `UserData` and stamp it from each source: proxy ⇒
   `rp.project`, send ⇒ `reqBody.Project`.

2. **Send-addressing (Slice 1).** Expose an optional `project` arg on the send
   tools (`sendHttpRequest`, `mirror`, `sendRaw`). It sets `reqBody.Project`,
   so a B-addressed send logs to B's handle **without** mutating the Active
   target — no switch, no switch-back, no race.

3. **Per-project session jars (Slice 2).** Add `project` to `_sessions`; change
   `UNIQUE(name)` → `UNIQUE(project, name)`; make "active" per-project. Resolve
   the jar by the request's project so a B-addressed send injects B's cookies and
   captures into A update A's jar. Schema migration backfills existing rows to a
   default project.

4. **Cross-jar cookie copy (Slice 3).** A thin, explicit
   `session(action:"copyCookies", from:{project,session}, to:{project,session},
   names?, rewriteDomain?)` — sugar over the existing `getCookies` +
   `updateCookies`. Isolation by default; sharing only by deliberate call.

## Consequences

- **Concurrent, conflict-free.** A browser/Serval keeps streaming into A
  (source-bound) while addressed sends land in B — simultaneously, no global to
  fight over. Two browsers on two projects capture in parallel for free.
- **Backward compatible.** Empty project ⇒ Active handle and the global jar,
  exactly as today. Proxy/repeater/REST callers that set no project are
  unchanged. The Active project remains the UI's default write target.
- **The UI view stays decoupled** from the agent's send target — the existing
  `viewedDB` read-path (`apps/app/mcp_project.go:118-121`) already separates the
  human's view from writes; nothing the agent addresses moves it.
- **Migration risk (Slice 2).** `UNIQUE(name)` → `UNIQUE(project, name)` needs a
  rebuild-and-backfill (SQLite can't drop a UNIQUE in place — same dance as the
  v5 `request_hash` migration at `mcp_project.go:291`). Existing sessions map to
  a default project; no silent constraint drop.
- **Cookie domain scoping (Slice 3).** A host-bound cookie copied to a jar used
  against a different host won't be sent unless re-scoped; the copy op exposes
  `rewriteDomain`. Most meaningful for shared-IdP/SSO or bearer tokens.
- **Out of scope here:** per-project `scope` and `match/replace` (same
  add-column-and-resolve pattern, deferred to a follow-up ADR); concurrent
  scope-routed *capture fan-out* (write one captured request to several projects
  on overlap) — novel, no prior art, tracked as a research spike, not built now.

## Alternatives rejected

- **Faster global `setActive`.** Doesn't help: with one global, 3–4 concurrent
  projects + a watching human always clobber. The fix is removing the shared
  global, not speeding up the switch.
- **One lorg process per project + router.** Heavyweight (ports/proxies/browsers
  per project), resource-hungry, defeats "quick." Rejected.
- **`setActive(B) → send → setActive(A)` wrapper.** Racy by construction against
  live capture; the exact trap the operator hits.
