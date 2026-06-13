---
artifact: ship
version: 1
status: ready
owners: [campbellcharlie]
last_updated: 2026-06-05
---

# Ship — ServalSync project tagging + startup alignment

## Release Scope

Localized fix (6 files, +70/-5) so Serval-mirrored traffic is returned by
`query project:<id>` and lorg's active project DB aligns with the Serval read
source on startup. See `.devfw/decisions/ADR-001-servalsync-project-tagging.md`
and `.devfw/iterations/001-servalsync-project-tagging.md`. No schema change
(`_data.project` already exists), no API break, no data migration.

## Environments and Configuration

- New flag `-project <id>` (optional). When omitted, the project is derived from
  the `-serval-db` path's `.../projects/<id>/` segment. When neither resolves,
  behavior is identical to today (untagged import, no alignment).
- No new env vars, ports, or secrets.

## Observability and Alerts

- Startup log: `[Startup] Active project aligned to Serval source: "<id>"` (or a
  failure line) confirms alignment.
- Existing `[ServalSync] imported N row(s), watermark=…` line unchanged.
- Smoke check: `query project:<id>` returns a non-zero count once Serval rows
  flow.

## Security and Compliance

- No change to auth, network exposure, or the read-only Serval DB handle
  (`mode=ro&query_only(true)` unchanged). `-project`/derived `<id>` only flows
  into a SQLite column value and a sanitized DB filename
  (`sanitizeProjectName`), not into raw SQL.

## Accessibility Signoff

N/A — backend traffic-ingestion change, no user-facing surface.

## Performance Signoff

No new per-row work beyond a single column set; import path unchanged
otherwise. No measurable impact.

## Rollout Plan

1. Build to `~/src/lorg/build` only — do NOT overwrite the live binary at
   `~/src/pentest-framework/libs/bin/lorg` (symlink → `~/src/lorg/lorg-bin`).
   The live lorg on :8090 stays up.
2. When ready to adopt, the operator restarts their own lorg instance with
   `-serval-db .../projects/<id>/traffic.db` (and optionally `-project <id>`).
3. Verify the startup alignment log line and that `query project:<id>` returns
   Serval rows.

## Rollback Plan

Revert the commit (or run the prior binary). No persisted state shape changed,
so rows tagged during the rollout simply carry a `project` value that older
binaries ignore — no cleanup required.

## Support and On-Call

- If `query project:<id>` still returns 0: confirm the resolved project (startup
  log) matches the queried `<id>`; if the Serval path lacks a `projects/<id>/`
  segment, pass `-project <id>` explicitly.

## Open Risks

- Active-project side effect (intended): `-serval-db` repoints the global write
  target to `<id>.db` for the process lifetime. Documented in iteration 001.
- Silent degrade to untagged when no project resolves (Deferred; `-project` is
  the escape hatch).

## Approvals

- Engineering: campbellcharlie — code + tests + doubt-driven review complete.
- Build clean (`go build ./...`); `go test ./...` green; live binary untouched.
