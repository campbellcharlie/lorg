package app

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	"gopkg.in/yaml.v2"
)

// ---------------------------------------------------------------------------
// ScopeManager -- in-memory scope rule engine
// ---------------------------------------------------------------------------

// ScopeRule defines a single scope inclusion or exclusion pattern.
type ScopeRule struct {
	Protocol string `json:"protocol" yaml:"protocol"` // http, https, or empty for any
	Host     string `json:"host" yaml:"host"`         // exact or glob pattern (e.g. *.example.com)
	Port     string `json:"port" yaml:"port"`         // port or empty for any
	Path     string `json:"path" yaml:"path"`         // path prefix or empty for any
	Reason   string `json:"reason,omitempty" yaml:"reason"` // why this rule exists
}

// ScopeManager holds include and exclude rules and provides thread-safe
// scope-checking against parsed URLs.
type ScopeManager struct {
	mu       sync.RWMutex
	includes []ScopeRule
	excludes []ScopeRule
}

// NewScopeManager returns an initialised ScopeManager with empty rule sets.
func NewScopeManager() *ScopeManager {
	return &ScopeManager{
		includes: make([]ScopeRule, 0),
		excludes: make([]ScopeRule, 0),
	}
}

// scopeManager is the package-level scope manager instance.
// Package-level because action-dispatch handlers access it without Backend reference.
// Thread-safe: ScopeManager uses an internal RWMutex for all operations.
var scopeManager = NewScopeManager()

// ---------------------------------------------------------------------------
// Host matching helper
// ---------------------------------------------------------------------------

// matchHost checks whether host matches pattern. Supports exact match and a
// single leading wildcard (*.example.com matches sub.example.com).
func matchHost(pattern, host string) bool {
	if pattern == host {
		return true
	}
	// Simple glob: *.example.com matches sub.example.com
	if strings.HasPrefix(pattern, "*.") {
		suffix := pattern[1:] // .example.com
		return strings.HasSuffix(host, suffix)
	}
	return false
}

// matchRule returns true if the parsed URL components match the given rule.
func matchRule(rule ScopeRule, scheme, host, port, path string) bool {
	// Host is mandatory in a rule -- must match.
	if rule.Host != "" && !matchHost(rule.Host, host) {
		return false
	}
	// Protocol check (empty means any).
	if rule.Protocol != "" && rule.Protocol != scheme {
		return false
	}
	// Port check (empty means any).
	if rule.Port != "" && rule.Port != port {
		return false
	}
	// Path prefix check (empty means any).
	if rule.Path != "" && !strings.HasPrefix(path, rule.Path) {
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// ScopeManager methods
// ---------------------------------------------------------------------------

// IsInScope determines whether rawURL falls within the loaded scope.
// Returns (inScope, reason).
func (sm *ScopeManager) IsInScope(rawURL string) (bool, string) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if len(sm.includes) == 0 {
		return false, "no scope rules loaded -- load a scope file first"
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return false, fmt.Sprintf("invalid URL: %v", err)
	}

	scheme := u.Scheme
	host := u.Hostname()
	port := u.Port()
	path := u.Path

	// Infer default port from scheme when not explicit.
	if port == "" {
		switch scheme {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}

	// 1. Must match at least one include rule.
	matched := false
	for _, rule := range sm.includes {
		if matchRule(rule, scheme, host, port, path) {
			matched = true
			break
		}
	}
	if !matched {
		return false, "URL does not match any include rule"
	}

	// 2. If any exclude rule matches, the URL is out of scope.
	for _, rule := range sm.excludes {
		if matchRule(rule, scheme, host, port, path) {
			reason := "matched exclude rule"
			if rule.Reason != "" {
				reason = rule.Reason
			}
			return false, reason
		}
	}

	return true, "in scope"
}

// AddRule appends a rule to the include or exclude list.
func (sm *ScopeManager) AddRule(ruleType string, rule ScopeRule) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	switch ruleType {
	case "include":
		sm.includes = append(sm.includes, rule)
	case "exclude":
		sm.excludes = append(sm.excludes, rule)
	}
}

// RemoveRule removes the rule at index from the specified list.
func (sm *ScopeManager) RemoveRule(ruleType string, index int) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	switch ruleType {
	case "include":
		if index < 0 || index >= len(sm.includes) {
			return fmt.Errorf("index %d out of range (0..%d)", index, len(sm.includes)-1)
		}
		sm.includes = append(sm.includes[:index], sm.includes[index+1:]...)
	case "exclude":
		if index < 0 || index >= len(sm.excludes) {
			return fmt.Errorf("index %d out of range (0..%d)", index, len(sm.excludes)-1)
		}
		sm.excludes = append(sm.excludes[:index], sm.excludes[index+1:]...)
	default:
		return fmt.Errorf("unknown rule type: %s (use 'include' or 'exclude')", ruleType)
	}
	return nil
}

// Reset clears all scope rules.
func (sm *ScopeManager) Reset() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.includes = make([]ScopeRule, 0)
	sm.excludes = make([]ScopeRule, 0)
}

// GetRules returns defensive copies of the include and exclude rule slices.
func (sm *ScopeManager) GetRules() (includes []ScopeRule, excludes []ScopeRule) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	inc := make([]ScopeRule, len(sm.includes))
	copy(inc, sm.includes)

	exc := make([]ScopeRule, len(sm.excludes))
	copy(exc, sm.excludes)

	return inc, exc
}

// ---------------------------------------------------------------------------
// YAML scope file schema (subset used for parsing)
// ---------------------------------------------------------------------------

type scopeFileTarget struct {
	URL            string   `yaml:"url"`
	AdditionalURLs []string `yaml:"additional_urls"`
}

type scopeFileExclusion struct {
	Path   string `yaml:"path"`
	Reason string `yaml:"reason"`
	Host   string `yaml:"host"`
}

type scopeFileRules struct {
	Exclusions []scopeFileExclusion `yaml:"exclusions"`
}

type scopeFile struct {
	Target scopeFileTarget `yaml:"target"`
	Rules  scopeFileRules  `yaml:"rules"`
}

// parseURLToRule extracts protocol, host, and port from a URL string and
// returns a ScopeRule suitable for the include list.
func parseURLToRule(rawURL string) (ScopeRule, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ScopeRule{}, fmt.Errorf("invalid URL %q: %v", rawURL, err)
	}

	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}

	return ScopeRule{
		Protocol: u.Scheme,
		Host:     u.Hostname(),
		Port:     port,
	}, nil
}

// ---------------------------------------------------------------------------
// Input schemas (struct-based, type-safe)
// ---------------------------------------------------------------------------

type ScopeLoadArgs struct {
	FilePath string `json:"filePath" jsonschema:"required" jsonschema_description:"Path to scope.yaml file"`
}

type ScopeCheckArgs struct {
	Url string `json:"url" jsonschema:"required" jsonschema_description:"URL to check against scope"`
}

type ScopeCheckMultipleArgs struct {
	Urls []string `json:"urls" jsonschema:"required" jsonschema_description:"URLs to check"`
}

type ScopeGetRulesArgs struct{}

type ScopeAddRuleArgs struct {
	Type     string `json:"type" jsonschema:"required" jsonschema_description:"include or exclude"`
	Host     string `json:"host" jsonschema:"required" jsonschema_description:"Host pattern (exact or glob with *)"`
	Protocol string `json:"protocol,omitempty" jsonschema_description:"http, https, or empty for any"`
	Port     string `json:"port,omitempty" jsonschema_description:"Port number or empty for any"`
	Path     string `json:"path,omitempty" jsonschema_description:"Path prefix or empty for any"`
	Reason   string `json:"reason,omitempty" jsonschema_description:"Reason for this rule"`
}

type ScopeRemoveRuleArgs struct {
	Type  string `json:"type" jsonschema:"required" jsonschema_description:"include or exclude"`
	Index int    `json:"index" jsonschema:"required" jsonschema_description:"Rule index to remove (0-based)"`
}

type ScopeResetArgs struct {
	Confirm bool `json:"confirm" jsonschema:"required" jsonschema_description:"Must be true to confirm scope reset"`
}

// ---------------------------------------------------------------------------
// Tool handlers
// ---------------------------------------------------------------------------

func (backend *Backend) scopeLoadHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ScopeLoadArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	data, err := os.ReadFile(args.FilePath)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to read scope file: %v", err)), nil
	}

	var sf scopeFile
	if err := yaml.Unmarshal(data, &sf); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to parse scope YAML: %v", err)), nil
	}

	// Reset existing rules before loading.
	scopeManager.Reset()

	// Build include rules from target URLs.
	allURLs := make([]string, 0, 1+len(sf.Target.AdditionalURLs))
	if sf.Target.URL != "" {
		allURLs = append(allURLs, sf.Target.URL)
	}
	allURLs = append(allURLs, sf.Target.AdditionalURLs...)

	for _, rawURL := range allURLs {
		rule, err := parseURLToRule(rawURL)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid target URL: %v", err)), nil
		}
		scopeManager.AddRule("include", rule)
	}

	// Build exclude rules from exclusions.
	for _, exc := range sf.Rules.Exclusions {
		rule := ScopeRule{
			Path:   exc.Path,
			Reason: exc.Reason,
			Host:   exc.Host,
		}
		scopeManager.AddRule("exclude", rule)
	}

	includes, excludes := scopeManager.GetRules()

	return mcpJSONResult(map[string]any{
		"success":    true,
		"includes":   includes,
		"excludes":   excludes,
		"totalRules": len(includes) + len(excludes),
	})
}

func (backend *Backend) scopeCheckHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ScopeCheckArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	inScope, reason := scopeManager.IsInScope(args.Url)

	return mcpJSONResult(map[string]any{
		"url":     args.Url,
		"inScope": inScope,
		"reason":  reason,
	})
}

func (backend *Backend) scopeCheckMultipleHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ScopeCheckMultipleArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	type checkResult struct {
		URL     string `json:"url"`
		InScope bool   `json:"inScope"`
		Reason  string `json:"reason"`
	}

	results := make([]checkResult, 0, len(args.Urls))
	inScopeCount := 0
	outOfScopeCount := 0

	for _, u := range args.Urls {
		inScope, reason := scopeManager.IsInScope(u)
		results = append(results, checkResult{
			URL:     u,
			InScope: inScope,
			Reason:  reason,
		})
		if inScope {
			inScopeCount++
		} else {
			outOfScopeCount++
		}
	}

	return mcpJSONResult(map[string]any{
		"results":         results,
		"inScopeCount":    inScopeCount,
		"outOfScopeCount": outOfScopeCount,
	})
}

func (backend *Backend) scopeGetRulesHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	includes, excludes := scopeManager.GetRules()

	return mcpJSONResult(map[string]any{
		"includes":   includes,
		"excludes":   excludes,
		"totalRules": len(includes) + len(excludes),
	})
}

func (backend *Backend) scopeAddRuleHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ScopeAddRuleArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	if args.Type != "include" && args.Type != "exclude" {
		return mcp.NewToolResultError("type must be 'include' or 'exclude'"), nil
	}

	rule := ScopeRule{
		Protocol: args.Protocol,
		Host:     args.Host,
		Port:     args.Port,
		Path:     args.Path,
		Reason:   args.Reason,
	}

	scopeManager.AddRule(args.Type, rule)

	return mcpJSONResult(map[string]any{
		"success": true,
		"type":    args.Type,
		"rule":    rule,
	})
}

func (backend *Backend) scopeRemoveRuleHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ScopeRemoveRuleArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	if args.Type != "include" && args.Type != "exclude" {
		return mcp.NewToolResultError("type must be 'include' or 'exclude'"), nil
	}

	if err := scopeManager.RemoveRule(args.Type, args.Index); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	return mcpJSONResult(map[string]any{
		"success":      true,
		"type":         args.Type,
		"removedIndex": args.Index,
	})
}

func (backend *Backend) scopeResetHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ScopeResetArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	if !args.Confirm {
		return mcp.NewToolResultError("confirm must be true to reset scope"), nil
	}

	scopeManager.Reset()

	return mcpJSONResult(map[string]any{
		"success": true,
		"message": "All scope rules cleared",
	})
}
