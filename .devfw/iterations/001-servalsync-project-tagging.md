---
artifact: iteration
version: 1
status: complete
owners: [campbellcharlie]
last_updated: 2026-06-05
---

# Iteration 001 — ServalSync project tagging + startup alignment

## Goal

Make Serval-mirrored traffic visible to project-scoped reads
(`query project:<id>` returned 0 for Serval rows while unfiltered returned
everything), and align lorg's active project DB with the Serval read source on
startup so consumers no longer have to restart lorg mid-session. See
`.devfw/decisions/ADR-001-servalsync-project-tagging.md`.

## Changes

- `internal/types/api.go` — add `Project string` to `AddRequestBodyType`.
- `apps/app/request.go` — `SaveRequestToBackend` sets `dataRecord.Set("project",
  reqBody.Project)` after `Load`, mirroring the proxy's `_data` tag
  (`proxy_rawproxy.go:495`). Empty is the untagged default → REST add and
  repeater are unchanged.
- `apps/app/serval_sync.go` — `ServalSync.project` field; `NewServalSync` takes a
  `project`; each imported row carries `Project: s.project`; new
  `DeriveProjectFromDBPath` extracts `<id>` from `.../projects/<id>/traffic.db`.
- `apps/app/mcp_project.go` — exported `SetActiveProject(name)` wrapping
  `projectDB.SetProject(name, "")`.
- `cmd/lorg/main.go` — `-project` flag (override; empty = derive); threaded to
  `serve`.
- `cmd/lorg/serve.go` — resolve project (flag > derive), call
  `SetActiveProject` before `Serve()`, pass project into `NewServalSync`.
- `apps/app/serval_sync_project_test.go` — acceptance + regression tests.

## Evidence

- `go build ./...` clean (exit 0). Optional binary built to `build/lorg`; live
  binary `lorg-bin` (symlink target of `~/src/pentest-framework/libs/bin/lorg`)
  left untouched (mtime unchanged).
- `go test ./apps/app/ ./internal/lorgdb/` green.
  - `TestServalSyncSavePathIsProjectQueryable`: a row saved via the ServalSync
    path (`SaveRequestToBackend` + `Project`) is returned by `project:<id>` and
    absent from a different project's query — the direct acceptance proof.
  - `TestSaveRequestUntaggedStaysUnscoped`: untagged callers stay out of scoped
    queries but remain in unscoped ones (backward-compat guard).
  - `TestDeriveProjectFromDBPath`: derive edges (trailing slash, nested,
    multiple/no `projects` segment, empty).
- Doubt-driven fresh-context adversarial review: no blocking findings; all four
  contract clauses verified against source. Concerns (sanitization divergence,
  active-project side effect, ancient-DB migration) reviewed and dispositioned
  in this iteration's notes.

## Remaining Risks

- **Active-project side effect (Won't-Fix / intended):** passing `-serval-db`
  with a resolvable `<id>` repoints the global active write-target DB to
  `<id>.db` for the process lifetime, so auto-proxy/repeater traffic also writes
  there. This is exactly the alignment the work package requested; the Serval
  rows do land in `<id>.db` via `LogTraffic`.
- **Silent degrade (Deferred):** if `-project` is omitted and the path has no
  `.../projects/<id>/` segment, the project resolves to `""` and rows import
  untagged — only a log line, no error. Acceptable: matches today's behavior;
  the `-project` flag is the explicit escape hatch.
- **Pre-existing `CounterManager` nil-deref (Won't-Fix):** untouched; ServalSync
  uses `Index==0` and never reaches that branch.
