---
artifact: iteration
version: 1
status: complete
owners: [campbellcharlie]
last_updated: 2026-06-13
---

# Iteration 003 — Slice 2: per-project session/cookie jars

## Goal

Make a B-addressed send inject **B's** cookies/CSRF, not the global jar — so the
Slice-1 routing actually carries the right auth. Sessions were global and flat
(`_sessions UNIQUE(name)`, one global `active` row); now they are scoped to a
project. See ADR-002.

## Changes

- `internal/lorgdb/migrate.go` — migration 6: `ALTER TABLE _sessions ADD COLUMN
  project`; uniqueness moves from `idx_sessions_name (name)` to
  `idx_sessions_project_name (project, name)`. Existing rows backfill to
  `project=''` (the default project) and keep their names → no index collision.
  Cheap because uniqueness was an INDEX, not a table constraint (no rebuild).
- `apps/app/mcp_sessions.go` — project-aware helpers
  `findActiveSessionInProject` / `findSessionByNameInProject` /
  `resolveSessionInProject`, with the bare `findActiveSession` /
  `findSessionByName` / `resolveSession` kept as `project=""` wrappers so
  existing callers (authz, cookie, template, browser tools) operate on the
  default project unchanged. `project` added to the session arg structs;
  create stamps `project`, switch deactivates only within the project, delete +
  csrfExtract + getHeaders + updateCookies scope by project, list reports it.
- `apps/app/mcp_consolidated.go` — `project` on the live `session` tool;
  inlined updateCookies/getCookies/setCookie resolve via the InProject helper.
- `apps/app/mcp_templates.go` — `injectSessionIntoRequestForProject` (jar
  injection by project); `injectSessionIntoRequest` kept as a `""` wrapper.
- `apps/app/mcp_http.go` — `injectSessionAndCSRF` and
  `captureSessionFromResponse` take a `project`; `sendHttpRequest` passes
  `args.Project`, so an addressed send injects AND captures into that project's
  jar.

## Evidence

- `go build ./...` clean (exit 0).
- `go test ./apps/app/ ./internal/lorgdb/` green.
  - `TestSessionsAreProjectScoped`: same name `auth` coexists in the default
    project and `ProjB` (migration moved uniqueness to (project,name)); each
    project has its own active session simultaneously; resolving by project
    returns that project's `sid`; an empty project resolves to an error, not
    another project's jar. The direct Slice-2 acceptance proof.
  - Migration 6 applies cleanly on a fresh migrated DB (log shows step 6).
  - All prior tests (Slice 1 routing, ADR-001 serval) still green — the `""`
    wrappers preserve every existing caller's behavior.

## Remaining Risk / Follow-ups

- **Cross-jar cookie copy (Slice 3)** — explicit `copyCookies` over
  `getCookies`/`updateCookies` with a `names` allowlist + optional
  `rewriteDomain`. Next iteration.
- The viewer-write guard and `setActive`-vs-registry note from iteration 002
  still stand.
- Per-project `scope` + `match/replace` remain global (future ADR-003).
