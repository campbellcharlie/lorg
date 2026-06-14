---
artifact: decision
version: 1
status: accepted
owners: [campbellcharlie]
last_updated: 2026-06-14
---

# ADR-003 Project management + write-path hardening

## Context

ADR-002 made projects addressable (per-call routing, per-project jars). Two gaps
remain, surfaced by reading the hot paths and running the MCP surface live:

**Write-path bottlenecks (reasoned from code, to be confirmed under load):**
- `LogTraffic` holds one `p.mu` across all DB I/O (`mcp_project.go:351+`), so
  every captured row serializes on a single lock even when routing to different
  project handles.
- The write-handle registry added in ADR-002 is unbounded (no cap/eviction).
- Capture logging is fire-and-forget `go LogTraffic(...)` — unbounded goroutines,
  no backpressure, errors dropped (`request.go`, `proxy_rawproxy.go`).
- Each row is 3 separate `Exec`s (http_traffic + http_messages + traffic_fts),
  no transaction → 3 WAL syncs per row.
- `findActiveSessionInProject` runs per send (inject + CSRF + capture).
- Bodies are stored twice (http_messages + traffic_fts content).
- Every captured row is dual-written to lorgdb `_data` AND projectDB — 2× cost
  and the consistency hazard behind the ADR-001 serval bug.

**Project lifecycle is half-built:**
- REST has create/list/info/active/switch — but no delete, archive, or rename.
- The MCP `project` tool can't create/list/delete at all (settings only).
- The `_projects` table exists (`migrate.go:543`) but is never read or written;
  the project list is inferred by scanning `.db` files on disk.

## Decision

Harden the write path and build a real project-management layer, delivered as
small reversible stages (one commit each — see `plan.md`).

1. **Narrow the write lock + a shared handle-lifecycle primitive.** Resolve/cache
   the write handle under `p.mu`, release before the inserts (`*sql.DB` is
   pool-safe). Add `closeHandlesForProject(name)` used by BOTH registry eviction
   and project delete.
2. **Bound the registry.** LRU cap on open write handles; evict via the primitive.
3. **One transaction per row** in `LogTraffic`.
4. **Bounded async logging.** A channel + worker pool replaces per-request
   goroutine spawn; backpressure + batching; surfaced error counter.
5. **Per-project active-session cache**, invalidated on switch.
6. **`_projects` as the source of truth.** Authoritative registry with metadata
   (created, last_active, target_host, status, notes, traffic_count, size_bytes);
   list/info read from it, not the filesystem.
7. **Archive-first lifecycle, guarded hard-delete.** Archive = status flag (data
   kept, hidden from active list). Hard delete requires explicit confirm, refuses
   the active project or any in-use project, tears down handles via the primitive,
   and removes `.db`+`-wal`+`-shm` together. Engagement data is evidence — destroy
   only on explicit intent.
8. **In-use detection.** A project bound by a proxy/browser/serval is "in use";
   destructive ops are refused.
9. **Project management on MCP.** `project` tool gains create/list/archive/delete/
   setActive so an agent manages engagements end-to-end.
10. **Retention accounting.** size + last_active per project; optional auto-archive
    of stale projects.
11. **Per-connection MCP project context.** A sticky per-connection default so an
    agent's burst of sends doesn't repeat `project:` each call.
12. **FTS external-content.** Make `traffic_fts` an external-content table over
    `http_messages` to remove duplicate body storage.
13. **Dual-write consolidation (strategic).** Make projectDB authoritative for
    traffic; reduce/retire the lorgdb `_data` write. Largest/riskiest — gated
    behind the rest and may be split into its own follow-up.

## Consequences

- Cross-project captures stop contending on one lock; logging gains backpressure
  and batching; per-row sync cost drops.
- Projects become first-class objects with safe lifecycle, not bare `.db` files.
- Destructive delete is opt-in and guarded; archive is the safe default.
- Backward compatible: empty/unspecified project still resolves to Active; the
  registry backfills from existing on-disk `.db` files on first boot.

## Validation

A load stage (serval feed + `fuzz`/`raceTest` driving volume through the live
MCP) must show, under sustained high traffic:
- no dropped traffic rows vs. requests issued (capture completeness),
- bounded goroutines + open handles (no leak/blowup),
- cross-project writes not serialized by the old lock,
- zero DB/`busy_timeout` errors, stable memory.
Iterate until these hold. Thresholds recorded in the load iteration log.

## Rejected / deferred

- Heavy PM (kanban/workflow/multi-user/RBAC) — out of scope; CRUD + metadata +
  safe lifecycle only.
- Per-project `scope`/`match-replace` config — folds onto the `_projects`
  registry later; not in this ADR's critical path.
