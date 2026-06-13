---
artifact: decision
version: 1
status: accepted
owners: [campbellcharlie]
last_updated: 2026-06-05
---

# ADR-001 Tag ServalSync traffic with a project and align the active project DB on startup

## Context

Traffic mirrored from Serval via `ServalSync` is invisible to project-scoped
reads. `query project:<id>` (the MCP `query` tool, `apps/app/mcp_query.go:574`)
filters the legacy `_data` table by `d.project` (spliced into the compiled
HTTPQL WHERE at `apps/app/mcp_query.go:520-522`). The `_data.project` column is
declared at `internal/lorgdb/migrate.go:237`.

Two write paths populate `_data`:

- **Proxy** builds the `_data` record directly and sets the tag:
  `apps/app/proxy_rawproxy.go:495` → `"project": rp.project` (the proxy carries
  `project` from `NewRawProxyWrapper`, `apps/app/proxy_rawproxy.go:85`).
- **ServalSync** saves via `Backend.SaveRequestToBackend`
  (`apps/app/serval_sync.go:163`) using `types.AddRequestBodyType`
  (`internal/types/api.go`), which has **no project field**. `SaveRequestToBackend`
  builds the `_data` record from the marshaled `UserData`
  (`apps/app/request.go:366-382`); `UserData` (`internal/types/userdata.go:38`)
  carries no project, so `_data.project` stays `''` and the rows fall outside any
  `project:<id>` query. `NewServalSync` (`apps/app/serval_sync.go:43`) and its
  call site (`cmd/lorg/serve.go:144`) carry no project identity.

Separately, the per-project SQLite DB read by the WebUI is the *Active* project
(`ProjectDB`, `apps/app/mcp_project.go:85-113`). On startup lorg opens
`TemporaryProject` (`InitProjectsDir` → `Init`, `apps/app/mcp_project.go:32`,
`118`), so even after tagging, ServalSync's dual-write (`LogTraffic`,
`apps/app/request.go:425`) lands in `TemporaryProject` rather than `<id>` unless
something later calls `SetProject` — the footgun that makes consumers restart
lorg mid-session.

## Decision

1. **Project identity for ServalSync.** Add `Project string` to
   `types.AddRequestBodyType`. In `SaveRequestToBackend`, set
   `dataRecord.Set("project", reqBody.Project)` — exactly mirroring the proxy's
   `_data` tag at `proxy_rawproxy.go:495`. Thread a `project` into
   `NewServalSync` and set `body.Project` on each imported row.

2. **Resolve the project name** from the `-serval-db` path's `.../projects/<id>/`
   segment (`DeriveProjectFromDBPath`), with a new `-project` flag override
   (derive-with-flag-override, per the brief).

3. **Startup alignment.** When a Serval project is resolved, call
   `SetActiveProject(<id>)` (`projectDB.SetProject`) before serving, so the
   read source (`.../projects/<id>/traffic.db`) and the write target (`<id>.db`)
   align without a later `SetProject` call or a restart.

## Consequences

- `query project:<id>` returns ServalSync rows; the WebUI's Active project also
  matches the Serval source on boot.
- **Backward compatible:** the other `SaveRequestToBackend` callers — the REST
  add endpoint (`request.go:260`) and the repeater (`repeater.go:47-56`) — leave
  `Project` unset, so `_data.project` stays `''` exactly as before. WebUI reads
  the project SQLite (`http_traffic`), not `_data.project`, so it is unaffected.
- If the path has no `/projects/<id>/` segment and no `-project` flag, the
  project resolves to `""`: ServalSync behaves exactly as today (untagged, no
  alignment) — no regression, the feature is simply unaligned.
