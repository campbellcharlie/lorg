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
- [x] **B1** Activate `_projects` as source of truth + metadata; backfill from
      on-disk `.db` files. (rec #9) — f2c2e01
- [x] **B2** In-use / binding detection. (rec #11) — ff518ef
- [x] **B3** Archive-first lifecycle + guarded hard-delete. (rec #10) — ff518ef
- [x] **B4** Project management on MCP (list/register/setActive/archive/unarchive/
      delete). (rec #12) — 4ac271c
- [x] **B5** Disk + retention accounting + auto-archive. (rec #13) — 9807c1b

## Phase C — ergonomics + structural
- [x] **C1** Per-connection MCP sticky default project. (rec #7) — cc04335
- [~] **C2** FTS external-content. (rec #6) — DEFERRED (Phase D evidence: no disk
      failure, search not a bottleneck; migration/trigger risk unjustified).
- [~] **C3** Dual-write consolidation. (rec #5) — DEFERRED to ADR-004 (Phase D
      evidence: dual-write caused zero failures and was not the limiter; retiring
      `_data` means migrating legacy query/UI/sitemap readers — large structural).

## Phase D — load validation — DONE (iteration 005)
- [x] **D** Concurrent load (10k @ c=100 ~1942/s, 4 projects) + 4 sustained rounds.

## Acceptance (load) — ALL MET
- [x] No dropped traffic rows vs requests issued. (captured == sent, exact)
- [x] Bounded goroutines + open handles. (RSS flat 288->290 MB over +15k req)
- [x] Cross-project writes not serialized. (4 parallel handles, no SQLITE_BUSY)
- [x] Zero DB/busy_timeout errors; stable memory. (0 errors across ~25k req)

## Rollback
- Each stage is one commit; `git revert <stage>` backs out a single stage.
- Schema migrations (C2, possibly B1) are additive/idempotent; revert the code,
  the migration stays applied harmlessly.

---

# Plan — Full _data retirement (ADR-004, Option 1) — Phase E

Strategy: make `http_traffic` a complete superset, build a cross-project UNION
read layer, migrate readers ONE AT A TIME (each keeps a `_data` fallback behind a
flag), and stop the `_data` write LAST. Build stays green at every commit.

- [x] **E1** Enrich `http_traffic` (fingerprint, generated_by, global_seq) +
      schema v6 migration; `LogTraffic` writes them. — 476754f
- [x] **E2** Cross-project read layer `unionTrafficRows` (merge/order/filter/limit
      across project DBs). — 0b376fa
- [x] **E3** searchTraffic metadata path + composite-id byte resolver. — a8ebc60
- [x] **E4** HTTPQL query via cross-project executor (unionCompiledQuery). — 4052938
- [x] **E5** clusterResponses + findAnomalies (Go grouping over union rows). — dfb6b47
- [x] **E6** authzTest traffic read. — a019e9e
- [x] **E7** mirror + getRequestResponseFromID + getRawRequestResponse + diff +
      extractor via rawBytesForID / getTrafficBytes. — 685d75c, 4ccba25...
- [x] **E8** generateWordlist, mapEndpoints, probeAuth, traffic-detail, project-list
      counts. — 83124fa, 4ccba25, ae0131c
- [x] **STRESS** concurrent read/write campaign — found + fixed 3 bugs. — 1257f1d
- [x] **E9 (mechanism)** `legacyDataWrites` gate (default ON). SaveRequestToBackend's
      _data/_req/_resp write is behind it; gate OFF ⇒ capture lives only in
      http_traffic, served by every migrated reader. Validated by
      TestLegacyDataWriteGate. — b0fea47
- [x] **E9 (full)** host-rows + traffic-list migrated; PROXY capture _data write
      gated; default flipped OFF. + durability fixes (open+insert under one lock;
      journal_mode=WAL only on new DB) → stress EXACT under churn. — 917345d
- [x] **E10** Migration 8 drops `_data/_req/_resp/_req_edited/_resp_edited/_attached/
      _raw`. proxy index counter tolerates the missing table. — 384329f

## ADR-004 COMPLETE
Every traffic reader is on the per-project http_traffic union; the legacy _data
write is off by default and the tables are dropped. Full suite + 40s stress green
under -race. Only residual _data reference: the proxy index counter, which now
tolerates the dropped table.

## Done (read side): every agent-facing traffic reader is on the union layer
searchTraffic (metadata+raw+regex), query (HTTPQL), clusterResponses, findAnomalies,
authzTest, mirror, getRequestResponseFromID, getRawRequestResponse, diffResponses,
extractor, generateWordlist, mapEndpoints, probeAuth, projectExport, websocket,
traffic-detail. + composite-id byte resolution + a -race stress campaign that found
and fixed 3 bugs.

## Phase F — serious stress campaign (after E)
Goal: BREAK lorg, find failure modes. Concurrent capture (proxy + send + serval)
+ cross-project union reads under load + delete/archive under load + session
churn + huge bodies + many projects (registry eviction) + sustained soak. Record
every failure; iterate to fix.
