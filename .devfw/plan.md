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

---

# Plan — PM + write-path hardening (ADR-003)

One commit per stage so any stage is independently revertible. Each stage:
build green + targeted test before the next.

## Phase A — write-path performance
- [x] **A1** Narrow the `LogTraffic` lock (RWMutex) + single-writer handles +
      `closeHandlesForProjectLocked` primitive. (rec #1) — 599d977
- [x] **A2** Bound the write-handle registry (FIFO cap, evict via primitive). (rec #2) — ce9bb5b
- [x] **A3** One transaction per row in `LogTraffic`. (rec #4) — faac706
- [x] **A4** Bounded async logging worker pool (backpressure, no unbounded
      goroutines). (rec #3) — 0d1291f
- [~] **A5** Per-project active-session cache. (rec #8) — DEFERRED, gated on Phase D
      evidence: cache invalidation spans ~8 session-mutation sites; only worth the
      correctness risk if the load test shows session lookups are hot.

## Phase B — project registry + management
- [ ] **B1** Activate `_projects` as source of truth + metadata; list/info read
      from it. Backfill from on-disk `.db` files on first boot. (rec #9)
- [ ] **B2** In-use / binding detection (proxy/browser/serval bound). (rec #11)
- [ ] **B3** Archive-first lifecycle + guarded hard-delete (confirm, refuse
      active/in-use, teardown via A1 primitive, remove db+wal+shm). (rec #10) [destructive]
- [ ] **B4** Project management on MCP (create/list/archive/delete/setActive). (rec #12)
- [ ] **B5** Disk + retention accounting (size, last_active, optional auto-archive). (rec #13)

## Phase C — ergonomics + structural
- [ ] **C1** Per-connection MCP project context (sticky default). (rec #7)
- [ ] **C2** FTS external-content over `http_messages` (kill body duplication). (rec #6) [migration]
- [ ] **C3** Dual-write consolidation: projectDB authoritative, reduce `_data`
      write. (rec #5) [biggest/riskiest — may split into ADR-004]

## Phase D — load validation
- [ ] **D** Load harness: serval feed + `fuzz`/`raceTest` at volume through live
      MCP. Monitor throughput, dropped rows, goroutine + handle counts, DB errors,
      memory. Iterate until acceptance (see ADR-003 Validation) holds.

## Acceptance (load)
- [ ] No dropped traffic rows vs requests issued.
- [ ] Bounded goroutines + open handles under sustained load.
- [ ] Cross-project writes not serialized by the old lock.
- [ ] Zero DB/busy_timeout errors; stable memory.

## Rollback
- Each stage is one commit; `git revert <stage>` backs out a single stage.
- Schema migrations (C2, possibly B1) are additive/idempotent; revert the code,
  the migration stays applied harmlessly.
