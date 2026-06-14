---
artifact: plan
version: 2
status: active
owners: [campbellcharlie]
last_updated: 2026-06-13
---

# Plan — Project-scoped contexts (ADR-002)

## Milestones

- **M1 Capture-binding + send-addressing** (Slice 1) — writes route by project;
  sends address a project per-call without mutating the Active target.
- **M2 Per-project session jars** (Slice 2) — auth follows the addressed project.
- **M3 Cross-jar cookie copy** (Slice 3) — explicit, selective jar transfer.

## Vertical Slices

### Slice 1 — Write routing + send-addressing  (this iteration)
- `internal/types/userdata.go`: add `Project string` (`db:"-"`, routing-only).
- `apps/app/mcp_project.go`: add write-handle registry
  (`writeHandles map[string]*sql.DB`) + `writeHandleForLocked(project)`; route
  `LogTraffic` to the resolved handle instead of `p.db`; close registry handles
  in `Close()`.
- `apps/app/request.go`: set `userdata.Project = reqBody.Project` before
  `LogTraffic`; expose `project` on the send tool arg path → `reqBody.Project`.
- `apps/app/proxy_rawproxy.go`: set `typed.Project = rp.project` before
  `LogTraffic` (capture stays bound to the proxy's project).
- **Default-preserving:** empty project ⇒ Active `p.db` (no behavior change).

### Slice 2 — Per-project session jars
- `internal/lorgdb/migrate.go`: `_sessions.project` column; `UNIQUE(name)` →
  `UNIQUE(project, name)` via rebuild+backfill (mirror v5 migration dance);
  per-project active.
- `apps/app/mcp_sessions.go`: project-scope create/list/switch/active resolution.
- `apps/app/request.go`: resolve the injected jar by the request's project.

### Slice 3 — Cross-jar cookie copy
- `apps/app/mcp_sessions.go`: `copyCookies` action over `getCookies` +
  `updateCookies`; `names` allowlist; optional `rewriteDomain`.

## Acceptance Criteria
- [x] A send addressed to B logs to `B.db` while the Active target stays A
      (verified: row present in B, absent from A; Active unchanged). — iter 002
- [x] Proxy capture bound to A keeps landing in A during a B-addressed send. — iter 002
- [x] Empty/unspecified project preserves today's behavior (regression guard). — iter 002/003
- [x] (S2) A B-addressed send injects B's active jar, not the global one. — iter 003
- [x] (S2) Existing sessions survive migration under a default project. — iter 003
- [x] (S3) `copyCookies` moves only allowlisted cookies. (No domain rewrite —
      lorg's jar is domain-less name->value; see iter 004.) — iter 004

## Dependencies
- Builds on ADR-001 (`reqBody.Project`, `_data.project`, `DeriveProjectFromDBPath`).

## Risks
- Write-handle registry leaks if not closed → close in `Close()`; cache by name.
- Session UNIQUE migration is destructive if backfill is wrong → rebuild in a tx,
  backfill to default project, stamp schema_version.
- Holding `p.mu` while opening a registry handle — acceptable (same as
  `openLocked`); keep the open path short.

## Release Strategy
- Ship per slice; each compiles green with targeted tests before the next.
- All slices preserve the empty-project default path for backward compatibility.

## Open Questions
- Should `setActive(B)` *promote* an existing registry handle instead of opening
  a second handle to `B.db`? (WAL makes two handles safe; defer unless contended.)
- Per-project `scope` + `match/replace`: same pattern — follow-up ADR-003?
