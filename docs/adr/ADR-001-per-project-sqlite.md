# ADR-001: Per-Project SQLite + Shared LorgDB Split

## Status
Accepted

## Context

Lorg needs to store two distinct categories of data:

1. **Traffic data** — HTTP requests/responses captured by the proxy. This is append-heavy, large, and project-scoped. Each engagement produces a separate corpus that should be isolated, portable, and independently deletable.

2. **Application state** — sessions, settings, scope rules, match/replace rules, templates. This is configuration-like: small, frequently read, and shared across the UI session.

## Decision

Use two SQLite databases per running instance:

- **Per-project `data.db`** (`<project-path>/lorg/pb_data/data.db`): owns traffic data (`_data` table) and project-specific collections. Switched when the user changes the active project via the UI. One file = one engagement = portable and independently archivable.

- **Shared `LorgDB`** (`internal/lorgdb`): owns application configuration (proxies, sessions, scope, templates, settings). Persists across project switches. Backed by the same SQLite library but a different file path.

## Alternatives Considered

- **Single database for everything** — rejected because mixing traffic (append-heavy, large) with config (small, frequently read) creates contention and makes per-project archiving awkward. A 50GB traffic DB and a 10KB config DB should not be the same file.

- **PostgreSQL** — rejected because lorg is a single-user desktop tool. PostgreSQL adds an external process dependency and operational overhead that provides no benefit for the single-writer workload.

- **In-memory store for config** — rejected because settings must survive process restart.

## Consequences

- Project switching is a file handle swap on `data.db`; no migration needed.
- Traffic data is portable: copy or archive the project directory.
- Code must be careful not to JOIN across the two databases (they are separate connections).
- WAL journal mode is set on both databases for concurrent read performance (proxy writes, UI reads simultaneously).
