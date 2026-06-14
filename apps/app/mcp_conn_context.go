package app

import (
	"context"
	"sync"

	mcpserver "github.com/mark3labs/mcp-go/server"
)

// Per-connection sticky default project (ADR-003 C1).
//
// When a tool call omits an explicit `project`, the calling MCP connection's
// sticky default is applied — so an agent working project B for a burst of sends
// sets it once (project useProject name:B) instead of repeating project:"B" on
// every call. Keyed by the mcp-go session id, so each connection has its own
// default and one agent's choice never affects another connection or the UI.
//
// Map entries are tiny strings and connections are few; entries are dropped when
// a connection clears its default. (A disconnect without an explicit clear leaves
// a small stale entry — acceptable for the connection counts lorg sees.)
var connProjectDefaults sync.Map // sessionID(string) -> project name(string)

func connectionSessionID(ctx context.Context) string {
	if s := mcpserver.ClientSessionFromContext(ctx); s != nil {
		return s.SessionID()
	}
	return ""
}

// setConnectionDefaultProject sets (or, with an empty name, clears) the sticky
// default project for the calling connection.
func setConnectionDefaultProject(ctx context.Context, name string) {
	id := connectionSessionID(ctx)
	if id == "" {
		return
	}
	if name == "" {
		connProjectDefaults.Delete(id)
		return
	}
	connProjectDefaults.Store(id, name)
}

// connectionDefaultProject returns the sticky default project for the calling
// connection, or "" if none is set.
func connectionDefaultProject(ctx context.Context) string {
	id := connectionSessionID(ctx)
	if id == "" {
		return ""
	}
	if v, ok := connProjectDefaults.Load(id); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
