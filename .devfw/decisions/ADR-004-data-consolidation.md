---
artifact: decision
version: 2
status: accepted
owners: [campbellcharlie]
last_updated: 2026-06-14
decision: "Option 1 — full _data retirement (chosen by owner over the recommended Option 2)"
---

> **DECISION (2026-06-14):** Owner chose **Option 1 — full `_data` retirement**,
> accepting the larger scope/risk over the recommended bounded Option 2. Executed
> incrementally (one reader at a time, each with a `_data` fallback) so the build
> and every tool stay working at every commit; the `_data` write is removed LAST,
> only after all readers are migrated and a stress campaign passes. Stage plan in
> `plan.md` (Phase E).

# ADR-004 Traffic store consolidation (the C3 deferral from ADR-003)

## Context

ADR-003 deferred C3 — "make projectDB authoritative, reduce the lorgdb `_data`
write" — as the biggest/riskiest item. A full read-only mapping of the dual-write
surface changes the recommendation.

### Findings (mapped, file:line in the investigation)

- **`_data` is the global cross-project index, not dead weight.** It is one table
  in lorgdb holding ALL projects' rows (with a `project` column); `http_traffic`
  is split across per-project `.db` files. Roughly ten MCP tools read ONLY from
  `_data` and have no per-project equivalent: `query` (HTTPQL), `searchTraffic`,
  `clusterResponses`/`findAnomalies`, `authz`, `mirror`, `getRequestResponseFromID`
  (raw bytes in lorgdb `_req`/`_resp`), websocket linking, the proxy index
  counter, and the project-list aggregation (`GROUP BY project`).
- **`fingerprint` lives only in `_data`** (not `http_traffic`); clustering/anomaly
  tools depend on it.
- **Retiring `_data` forces a "union N project DBs" read layer** for every
  cross-project query, plus a global ordering to replace `_data.index`, plus a
  fingerprint backfill — a multi-week change touching ~10 tools.
- **The dual-write is already synchronized**: every `_data` writer also calls
  `enqueueTraffic`, so the two stores stay consistent. The consistency concern
  cited in ADR-003 (the ADR-001 serval bug) was a *tagging* bug, since fixed — not
  an ongoing dual-write divergence.
- **The real remaining cost is byte duplication**, not the lightweight row
  metadata: a captured body is stored in lorgdb `_req`/`_resp`, in projectDB
  `http_messages`, AND again in `traffic_fts`. That is the write-amp + disk cost.

## Decision — options

**Option 1 — Full retirement of `_data` (the literal "C3").** Add a union-query
layer over all project DBs, add `fingerprint` + a global sequence to
`http_traffic`, backfill, and migrate ~10 readers. *Rejected for now:* 2–3 weeks,
high regression risk across the core read tools, for a benefit (single store) the
synchronized dual-write already delivers on consistency.

**Option 2 — Bounded, safe consolidation (RECOMMENDED).** Keep `_data` as the
legitimate global index; attack the real cost (byte duplication) and shrink
`_data`'s exclusive surface so a future Option 1 is cheaper:
- **C3a** Make `traffic_fts` an FTS5 **external-content** table over
  `http_messages` (removes the in-projectDB body duplication — this absorbs the
  old C2). [migration, projectDB schema]
- **C3b** Add `fingerprint` to `http_traffic` and write it in `LogTraffic`, so
  projectDB stops depending on `_data` for clustering and becomes a fuller
  per-project superset.
- Net: meaningful disk/write-amp reduction, no reader rewrites, no cross-project
  regression risk.

**Option 3 — No-op.** The dual-write is synchronized and not a load bottleneck
(ADR-003 Phase D). Do nothing; spend the effort on stress testing instead.

## Recommendation

**Option 2.** It captures the genuine win (kill body duplication) at low risk,
keeps the global index that ~10 tools rely on, and de-risks a future full
retirement — without a multi-week rewrite the evidence doesn't justify. Full
Option 1 stays documented as a future possibility, explicitly out of scope here.

## Validation

After C3a/C3b: existing search/cluster/query tests stay green; a new test proves
FTS still returns correct hits with external content; clustering can read
`fingerprint` from `http_traffic`. Then the broad stress campaign (separate plan)
hunts for failure modes under load.
