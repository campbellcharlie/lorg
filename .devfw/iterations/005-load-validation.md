---
artifact: iteration
version: 1
status: complete
owners: [campbellcharlie]
last_updated: 2026-06-14
---

# Iteration 005 — Phase D: load validation + evidence-gated deferrals

## Goal

Drive high-volume traffic through the live lorg+MCP and confirm the ADR-003
write-path work (A1-A4) holds: no dropped rows, no DB errors, bounded resources.
Use the evidence to decide the deferred stages (A5, C2, C3).

## Harness

- Live `lorg` (built from this branch) on a throwaway port; concurrent Go target.
- Concurrent load generator hammering `/api/repeater/send` (-> sendRepeaterLogic
  -> SaveRequestToBackend -> enqueueTraffic -> LogTraffic), round-robin across 4
  addressed projects to stress cross-project routing + the worker pool.
- Single-threaded python target first capped throughput at ~170/s; swapping to a
  concurrent target exposed lorg's real rate.

## Results (acceptance MET)

- **Throughput:** 10,000 req @ c=100 in 4.99s = **~1942 req/s** (11x the
  python-bottlenecked run).
- **Completeness:** captured rows == successful sends EXACTLY, per project
  (2423/2419/2426/2430 = 9698 = ok). **Zero dropped rows.** Across 4 sustained
  rounds (~25k attempted) cumulative capture stayed 1:1 with **zero**
  write-path errors (SQLITE_BUSY / insert / commit / tx all 0).
- **Cross-project concurrency:** 4 project handles written in parallel, no
  SQLITE_BUSY — A1 (RWMutex + SetMaxOpenConns(1)) confirmed.
- **Memory:** RSS flat across rounds (288 -> 290 MB over +15k requests) — no leak;
  A4 worker pool (8) + A2 handle cap (16) bound resources as designed.
- **PM + C1 live via MCP:** a real Claude MCP session ran project list /
  useProject / archive / delete and a no-project send; DB-verified — the sticky
  default routed to Load2, delete removed Load1's files + registry row, archive
  hid Load0.

## Evidence-gated decisions

- **A5 (session cache):** the load path didn't use injectSession, so session
  lookups were never hot. No evidence justifies the ~8-site invalidation risk.
  **Deferred.**
- **C2 (FTS external-content):** a disk optimization; no disk-related failure
  surfaced and search wasn't a bottleneck. Migration + trigger risk unjustified by
  evidence. **Deferred.**
- **C3 (dual-write consolidation):** `_data` received all 9698 rows (the dual
  write is real), but it caused **zero** failures and was not the throughput
  limiter — the per-request sitemap enrichment fetch (sitemap.go:76, pre-existing)
  and target round-trips dominated. Removing the `_data` write means migrating the
  legacy query/UI/sitemap readers off it — a large structural change the ADR
  pre-scoped to a follow-up. **Deferred to ADR-004.**

## Incidental findings (out of ADR-003 scope)

- `sitemap.go:76` does a synchronous `http.DefaultClient.Get` per captured request
  to the host root, stripping the port (hits :80, "connection refused"). Pre-
  existing; the bigger throughput drag than the dual-write. Worth its own fix.
- Repeater uses `Connection: close`; at c=100 the load generator hit ephemeral-
  port exhaustion (~3% send failures) — a load-gen artifact, not a lorg defect.

## Outcome

ADR-003 Validation criteria all satisfied. The write-path hardening (A1-A4) and
the project-management system (B1-B5) are load-validated and correct. A5/C2/C3
deferred with evidence; C3 tracked for ADR-004.
