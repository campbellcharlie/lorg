---
artifact: iteration
version: 1
status: complete
owners: [campbellcharlie]
last_updated: 2026-06-13
---

# Iteration 002 — Slice 1: per-project write routing + send-addressing

## Goal

Let an addressed send land in project B's traffic DB while a browser/Serval keeps
capturing into the Active project A — concurrently, with no global switch and no
race. Removes ADR-001's "global Active write-target" footgun for the routing path.
See `.devfw/decisions/ADR-002-project-scoped-contexts.md`.

## Changes

- `internal/types/userdata.go` — add `Project string` (`db:"-"`, routing-only;
  kept out of `_data` marshaling so the explicit `_data.project` tag is unaffected).
- `apps/app/mcp_project.go` — `ProjectDB.writeHandles` registry +
  `writeHandleForLocked(name)` (write-side twin of `readDBCache`, mirrors
  `openLocked`'s open/PRAGMA/schema steps); `LogTraffic` resolves the target
  handle from `userdata.Project` instead of always `p.db`; `Close()` releases
  registry handles. Empty/Active project ⇒ `p.db` (unchanged default).
- `apps/app/request.go` — `SaveRequestToBackend` sets
  `userdata.Project = reqBody.Project` before `LogTraffic`.
- `apps/app/proxy_rawproxy.go` — capture stamps `typed.Project = rp.project`, so
  each proxy listener stays bound to its own project.
- `apps/app/repeater.go` — `RepeaterSendRequest.Project` → `addReqBody.Project`.
  This is the shared chokepoint for every sender (sendHttpRequest, mirror,
  graphql, templates, authz), so one change routes them all.
- `apps/app/mcp_http.go` — expose optional `project` on `sendHttpRequest`
  (`SendHttpRequestArgs.Project`), threaded into the main + redirect sends.

## Evidence

- `go build ./...` clean (exit 0).
- `go test ./apps/app/ ./internal/lorgdb/` green.
  - `TestLogTrafficRoutesByProject`: Active=A; a `Project=B` row lands in `B.db`,
    default + explicit-active rows land in `A.db`, and `p.name` stays `A` — the
    direct ADR-002 acceptance proof.
  - `TestLogTrafficEmptyProjectUnchanged`: no project addressing ⇒ all rows in
    the single Active DB, zero addressed handles opened (backward-compat guard).
  - ADR-001 serval tests still green (no interference from the new `Project`
    field on `_data` tagging).

## Remaining Risks / Follow-ups

- **Session/cookie jar still global (Slice 2).** A B-addressed send currently
  injects the global active jar, not B's. Auth correctness needs per-project
  jars: `_sessions.project` + `UNIQUE(project,name)` + per-project active +
  resolve-by-request-project. Tracked in `plan.md` M2.
- **Viewer guard.** `sendHttpRequest` still rejects sends while the UI views a
  non-active project (`mcp_http.go:137`). Conservative and safe; an addressed
  send doesn't touch Active/Viewed, so this can be relaxed for addressed sends
  in a later refinement.
- **`setActive` vs registry.** Promoting an existing registry handle to Active is
  deferred — WAL makes a second handle to the same file safe.
- Per-project `scope` + `match/replace` deferred to ADR-003.
