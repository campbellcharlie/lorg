# ADR-002: Echo as the HTTP Framework

## Status
Accepted

## Context

Lorg needs an HTTP server framework for its REST API (~80 endpoints) and SSE connections. Requirements:

- Clean routing with path parameters (`/api/collections/:collection/records/:id`)
- Middleware support (auth, logging, CORS)
- Context object carrying request/response per-handler
- Streaming support for SSE (MCP server-sent events)
- Active maintenance with Go 1.21+ compatibility

## Decision

Use **Echo v4** (`github.com/labstack/echo/v4`).

## Alternatives Considered

- **`net/http` stdlib** — rejected because routing with path parameters requires significant hand-rolling. At 80+ endpoints, the maintenance cost outweighs the zero-dependency benefit.

- **Gin** — viable alternative with comparable feature set. Echo was chosen for its cleaner context API (`c echo.Context` vs Gin's `*gin.Context`) and explicit error return pattern (`return echo.NewHTTPError(...)`) which integrates better with Go's error handling idioms.

- **Chi** — lightweight and idiomatic but lacks built-in binding/validation helpers that reduce handler boilerplate.

- **Fiber** — fast but not `net/http` compatible, which would break integration with the MCP SSE server (`mark3labs/mcp-go`) that implements `http.Handler`.

## Consequences

- Error handling is explicit: handlers return `error`, Echo's error handler converts `*echo.HTTPError` to JSON.
- MCP SSE server integrates via `ServeHTTP` on the Echo route — no adapter needed.
- Echo's `HideBanner` is set to suppress startup noise in structured log output.
- Middleware chain order matters: trace ID → logging → routes. New middleware goes before `RegisterRoutes`.
