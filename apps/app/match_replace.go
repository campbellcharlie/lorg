package app

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/campbellcharlie/lorg/internal/lorgdb"
	"github.com/labstack/echo/v4"
	"github.com/mark3labs/mcp-go/mcp"
)

// MatchReplaceRule is a compiled match/replace rule.
type MatchReplaceRule struct {
	ID       string
	Type     string // request_header, request_body, response_header, response_body
	Match    *regexp.Regexp
	Replace  string
	Scope    string // host filter (empty = all)
	Comment  string
	Priority float64
	Enabled  bool
}

// MatchReplaceManager holds compiled rules and applies them to traffic.
type MatchReplaceManager struct {
	mu    sync.RWMutex
	rules []MatchReplaceRule
	db    *lorgdb.LorgDB
}

var matchReplaceMgr = &MatchReplaceManager{}

// InitMatchReplace loads rules from DB and compiles them.
func (backend *Backend) InitMatchReplace() {
	matchReplaceMgr.db = backend.DB
	matchReplaceMgr.Reload()
}

// Reload reloads all rules from the database.
func (m *MatchReplaceManager) Reload() {
	m.mu.Lock()
	defer m.mu.Unlock()

	records, err := m.db.FindRecordsSorted("_match_replace", "enabled = ?", "priority", 0, 0, true)
	if err != nil {
		m.rules = nil
		return
	}

	var rules []MatchReplaceRule
	for _, r := range records {
		pattern := r.GetString("match")
		re, err := regexp.Compile(pattern)
		if err != nil {
			log.Printf("[MatchReplace] Invalid regex in rule %s: %v", r.Id, err)
			continue
		}
		rules = append(rules, MatchReplaceRule{
			ID:       r.Id,
			Type:     r.GetString("type"),
			Match:    re,
			Replace:  r.GetString("replace"),
			Scope:    r.GetString("scope"),
			Comment:  r.GetString("comment"),
			Priority: r.GetFloat("priority"),
			Enabled:  r.GetBool("enabled"),
		})
	}
	m.rules = rules
	log.Printf("[MatchReplace] Loaded %d rules", len(rules))
}

// ApplyToRequest modifies an HTTP request in-place according to enabled rules.
func (m *MatchReplaceManager) ApplyToRequest(req *http.Request) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	host := req.Host
	for _, rule := range m.rules {
		if !rule.Enabled {
			continue
		}
		if rule.Scope != "" && !strings.Contains(host, rule.Scope) {
			continue
		}

		switch rule.Type {
		case "request_header":
			for key, vals := range req.Header {
				for i, v := range vals {
					if rule.Match.MatchString(key + ": " + v) {
						req.Header[key][i] = rule.Match.ReplaceAllString(v, rule.Replace)
					}
				}
			}
		case "request_body":
			if req.Body != nil {
				body, _ := io.ReadAll(req.Body)
				req.Body.Close()
				modified := rule.Match.ReplaceAll(body, []byte(rule.Replace))
				req.Body = io.NopCloser(bytes.NewReader(modified))
				req.ContentLength = int64(len(modified))
			}
		}
	}
}

// ApplyToResponse modifies an HTTP response in-place according to enabled rules.
func (m *MatchReplaceManager) ApplyToResponse(resp *http.Response) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	host := ""
	if resp.Request != nil {
		host = resp.Request.Host
	}

	for _, rule := range m.rules {
		if !rule.Enabled {
			continue
		}
		if rule.Scope != "" && !strings.Contains(host, rule.Scope) {
			continue
		}

		switch rule.Type {
		case "response_header":
			for key, vals := range resp.Header {
				for i, v := range vals {
					if rule.Match.MatchString(key + ": " + v) {
						resp.Header[key][i] = rule.Match.ReplaceAllString(v, rule.Replace)
					}
				}
			}
		case "response_body":
			if resp.Body != nil {
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				modified := rule.Match.ReplaceAll(body, []byte(rule.Replace))
				resp.Body = io.NopCloser(bytes.NewReader(modified))
				resp.ContentLength = int64(len(modified))
			}
		}
	}
}

// --- MCP Tool ---

type MatchReplaceArgs struct {
	Action  string `json:"action" jsonschema:"description=Operation: add, list, remove, enable, disable, reload"`
	ID      string `json:"id,omitempty" jsonschema:"description=Rule ID (for remove/enable/disable)"`
	Type    string `json:"type,omitempty" jsonschema:"description=Rule type: request_header, request_body, response_header, response_body"`
	Match   string `json:"match,omitempty" jsonschema:"description=Regex pattern to match"`
	Replace string `json:"replace,omitempty" jsonschema:"description=Replacement string (supports $1 capture groups)"`
	Scope   string `json:"scope,omitempty" jsonschema:"description=Host filter (empty = all hosts)"`
	Comment string `json:"comment,omitempty" jsonschema:"description=Description of what this rule does"`
}

func (backend *Backend) matchReplaceHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args MatchReplaceArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "add":
		if args.Match == "" || args.Type == "" {
			return mcp.NewToolResultError("match and type are required"), nil
		}
		if _, err := regexp.Compile(args.Match); err != nil {
			return mcp.NewToolResultError("invalid regex: " + err.Error()), nil
		}
		record := lorgdb.NewRecord("_match_replace")
		record.Set("type", args.Type)
		record.Set("match", args.Match)
		record.Set("replace", args.Replace)
		record.Set("scope", args.Scope)
		record.Set("comment", args.Comment)
		record.Set("enabled", true)
		if err := backend.DB.SaveRecord(record); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		matchReplaceMgr.Reload()
		return mcpJSONResult(map[string]any{"success": true, "id": record.Id, "comment": args.Comment})

	case "list":
		records, _ := backend.DB.FindRecords("_match_replace", "1=1")
		items := make([]map[string]any, 0)
		for _, r := range records {
			items = append(items, map[string]any{
				"id":      r.Id,
				"enabled": r.GetBool("enabled"),
				"type":    r.GetString("type"),
				"match":   r.GetString("match"),
				"replace": r.GetString("replace"),
				"scope":   r.GetString("scope"),
				"comment": r.GetString("comment"),
			})
		}
		return mcpJSONResult(map[string]any{"rules": items, "count": len(items)})

	case "remove":
		if args.ID == "" {
			return mcp.NewToolResultError("id is required"), nil
		}
		if err := backend.DB.DeleteRecord("_match_replace", args.ID); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		matchReplaceMgr.Reload()
		return mcpJSONResult(map[string]any{"success": true, "deleted": args.ID})

	case "enable", "disable":
		if args.ID == "" {
			return mcp.NewToolResultError("id is required"), nil
		}
		record, err := backend.DB.FindRecordById("_match_replace", args.ID)
		if err != nil {
			return mcp.NewToolResultError("rule not found"), nil
		}
		record.Set("enabled", args.Action == "enable")
		backend.DB.SaveRecord(record)
		matchReplaceMgr.Reload()
		return mcpJSONResult(map[string]any{"success": true, "id": args.ID, "enabled": args.Action == "enable"})

	case "reload":
		matchReplaceMgr.Reload()
		return mcpJSONResult(map[string]any{"success": true, "message": "rules reloaded"})

	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + " (use add, list, remove, enable, disable, reload)"), nil
	}
}

// --- REST endpoints ---

func (backend *Backend) MatchReplaceEndpoints(e *echo.Echo) {
	e.GET("/api/match-replace", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}
		records, _ := backend.DB.FindRecords("_match_replace", "1=1")
		items := make([]map[string]any, 0)
		for _, r := range records {
			item := map[string]any{"id": r.Id, "created": r.Created, "updated": r.Updated}
			for k, v := range r.Data {
				item[k] = v
			}
			items = append(items, item)
		}
		return c.JSON(http.StatusOK, map[string]any{"rules": items})
	})
}
