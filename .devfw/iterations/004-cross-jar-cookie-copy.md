---
artifact: iteration
version: 1
status: complete
owners: [campbellcharlie]
last_updated: 2026-06-13
---

# Iteration 004 — Slice 3: cross-jar cookie copy

## Goal

Let auth that genuinely spans projects (shared IdP/SSO, bearer tokens) move
between project jars by an explicit, selective call — isolation stays the
default. Completes ADR-002.

## Design note (read the implementation, not the assumption)

The earlier design sketch floated a `rewriteDomain` option. lorg's jar actually
stores cookies as a flat `name->value` map with **no per-cookie domain** (they're
injected as one blanket `Cookie:` header), so domain rewriting is a no-op here.
Dropped it rather than ship a dead parameter. Copy is a pure value merge; the
caller decides whether the cookies are meaningful against the destination target.

## Changes

- `apps/app/mcp_sessions.go` — `copyCookiesBetween(fromProject, fromName,
  toProject, toName, names)`: resolves each endpoint via `resolveSessionInProject`
  (empty name = that project's active session), merges allowlisted cookies
  (empty names = all) into the destination, preserves the destination's other
  cookies, leaves the source untouched, rejects same-session copies.
- `apps/app/mcp_consolidated.go` — `copyCookies` action + `fromProject/fromName/
  toProject/toName/names` fields on the session tool; the case is a thin wrapper
  over the helper.
- `apps/app/mcp.go` — session tool description notes per-project jars + the new
  action.

## Evidence

- `go build ./...` clean (exit 0).
- `go test ./apps/app/ ./internal/lorgdb/` green.
  - `TestCopyCookiesBetweenJars`: copies ONLY the allowlisted `sso` token from
    project A's active jar to project B's active jar; B's existing cookie is
    preserved, the non-allowlisted `acme_sid` does NOT leak, and A's jar is
    unchanged. The direct Slice-3 acceptance proof.
  - Full suite green; Slices 1–2 + ADR-001 tests unaffected.

## ADR-002 status

All three slices delivered and tested:
1. write routing + send-addressing (iteration 002)
2. per-project session jars (iteration 003)
3. cross-jar cookie copy (this)

Deferred (tracked, not built): relax the viewer-write guard for addressed sends;
`setActive` promoting a registry handle; per-project `scope` + `match/replace`
(future ADR-003); concurrent scope-routed capture fan-out (research spike).
