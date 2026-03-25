package app

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// withProjectDB -- helper to lock the mutex and validate readiness
// ---------------------------------------------------------------------------

// withProjectDB acquires projectDB.mu, checks that the database is open and
// ready, then executes fn with the live *sql.DB.  Returns an error if the
// project database has not been initialised yet (caller should surface this
// to the MCP client via mcp.NewToolResultError).
func withProjectDB(fn func(db *sql.DB) error) error {
	projectDB.mu.Lock()
	defer projectDB.mu.Unlock()
	if projectDB.db == nil || !projectDB.ready {
		return fmt.Errorf("no active project database -- use projectSetup first")
	}
	return fn(projectDB.db)
}

// ---------------------------------------------------------------------------
// Arg types
// ---------------------------------------------------------------------------

type TagTrafficArgs struct {
	RequestID int    `json:"requestId" jsonschema:"required" jsonschema_description:"request_id from project SQLite DB (http_traffic.request_id)"`
	Tag       string `json:"tag" jsonschema:"required" jsonschema_description:"Tag name (e.g. interesting, sqli-candidate, auth-bypass)"`
	Note      string `json:"note,omitempty" jsonschema_description:"Optional note for this tag"`
}

type GetTaggedTrafficArgs struct {
	Tag   string `json:"tag" jsonschema:"required" jsonschema_description:"Tag name to filter by"`
	Limit int    `json:"limit,omitempty" jsonschema_description:"Max results (default: 100)"`
}

type ListTagsArgs struct{}

type DeleteTrafficTagArgs struct {
	RequestID int    `json:"requestId" jsonschema:"required" jsonschema_description:"request_id to remove tag from"`
	Tag       string `json:"tag" jsonschema:"required" jsonschema_description:"Tag name to remove"`
}

// ---------------------------------------------------------------------------
// tagTraffic handler
// ---------------------------------------------------------------------------

func (backend *Backend) tagTrafficHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args TagTrafficArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	err := withProjectDB(func(db *sql.DB) error {
		_, execErr := db.Exec(
			"INSERT INTO traffic_tags (traffic_id, tag, note) VALUES (?, ?, ?)",
			args.RequestID, args.Tag, args.Note,
		)
		return execErr
	})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to tag traffic: %v", err)), nil
	}

	return mcpJSONResult(map[string]any{
		"success":   true,
		"requestId": args.RequestID,
		"tag":       args.Tag,
	})
}

// ---------------------------------------------------------------------------
// getTaggedTraffic handler
// ---------------------------------------------------------------------------

func (backend *Backend) getTaggedTrafficHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args GetTaggedTrafficArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	limit := args.Limit
	if limit <= 0 {
		limit = 100
	}

	type taggedItem struct {
		RequestID  int    `json:"requestId"`
		Method     string `json:"method"`
		Host       string `json:"host"`
		Path       string `json:"path"`
		StatusCode int    `json:"statusCode"`
		Tool       string `json:"tool"`
		Timestamp  string `json:"timestamp"`
		TagNote    string `json:"tagNote"`
	}

	var items []taggedItem

	err := withProjectDB(func(db *sql.DB) error {
		rows, queryErr := db.Query(`
			SELECT t.request_id, t.method, t.host, t.path, t.status_code,
			       t.tool, t.timestamp, tt.note
			FROM http_traffic t
			JOIN traffic_tags tt ON tt.traffic_id = t.request_id
			WHERE tt.tag = ?
			ORDER BY t.request_id DESC
			LIMIT ?`, args.Tag, limit)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()

		for rows.Next() {
			var item taggedItem
			var note sql.NullString
			if scanErr := rows.Scan(
				&item.RequestID, &item.Method, &item.Host, &item.Path,
				&item.StatusCode, &item.Tool, &item.Timestamp, &note,
			); scanErr != nil {
				return scanErr
			}
			if note.Valid {
				item.TagNote = note.String
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to get tagged traffic: %v", err)), nil
	}

	if items == nil {
		items = []taggedItem{}
	}

	return mcpJSONResult(map[string]any{
		"tag":   args.Tag,
		"items": items,
		"count": len(items),
	})
}

// ---------------------------------------------------------------------------
// listTags handler
// ---------------------------------------------------------------------------

func (backend *Backend) listTagsHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	type tagCount struct {
		Tag   string `json:"tag"`
		Count int    `json:"count"`
	}

	var tags []tagCount
	total := 0

	err := withProjectDB(func(db *sql.DB) error {
		rows, queryErr := db.Query(`
			SELECT tag, COUNT(*) as count
			FROM traffic_tags
			GROUP BY tag
			ORDER BY count DESC`)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()

		for rows.Next() {
			var tc tagCount
			if scanErr := rows.Scan(&tc.Tag, &tc.Count); scanErr != nil {
				return scanErr
			}
			total += tc.Count
			tags = append(tags, tc)
		}
		return rows.Err()
	})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to list tags: %v", err)), nil
	}

	if tags == nil {
		tags = []tagCount{}
	}

	return mcpJSONResult(map[string]any{
		"tags":  tags,
		"total": total,
	})
}

// ---------------------------------------------------------------------------
// deleteTrafficTag handler
// ---------------------------------------------------------------------------

func (backend *Backend) deleteTrafficTagHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args DeleteTrafficTagArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	err := withProjectDB(func(db *sql.DB) error {
		_, execErr := db.Exec(
			"DELETE FROM traffic_tags WHERE traffic_id = ? AND tag = ?",
			args.RequestID, args.Tag,
		)
		return execErr
	})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to delete traffic tag: %v", err)), nil
	}

	return mcpJSONResult(map[string]any{
		"success":   true,
		"requestId": args.RequestID,
		"tag":       args.Tag,
	})
}
