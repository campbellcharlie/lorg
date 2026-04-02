package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// Structural diff: header-by-header + body structure comparison
// ---------------------------------------------------------------------------

type StructuralDiffArgs struct {
	Response1     string   `json:"response1" jsonschema:"required" jsonschema_description:"First raw HTTP response"`
	Response2     string   `json:"response2" jsonschema:"required" jsonschema_description:"Second raw HTTP response"`
	IgnoreHeaders []string `json:"ignoreHeaders,omitempty" jsonschema_description:"Headers to ignore in comparison"`
}

func (backend *Backend) structuralDiffHandler(ctx context.Context, resp1, resp2 string, ignoreHeaders []string) (*mcp.CallToolResult, error) {
	status1, _, headers1, body1 := parseHTTPResponse(resp1)
	status2, _, headers2, body2 := parseHTTPResponse(resp2)

	ignoreSet := make(map[string]bool)
	for _, h := range ignoreHeaders {
		ignoreSet[strings.ToLower(h)] = true
	}

	// Status line diff
	statusDiff := map[string]any{
		"match":     status1 == status2,
		"response1": status1,
		"response2": status2,
	}

	// Header-by-header diff
	type headerDiff struct {
		Name   string `json:"name"`
		Status string `json:"status"` // "match", "differ", "only_in_1", "only_in_2"
		Value1 string `json:"value1,omitempty"`
		Value2 string `json:"value2,omitempty"`
	}

	allHeaders := make(map[string]bool)
	for k := range headers1 {
		allHeaders[strings.ToLower(k)] = true
	}
	for k := range headers2 {
		allHeaders[strings.ToLower(k)] = true
	}

	var headerDiffs []headerDiff
	for h := range allHeaders {
		if ignoreSet[h] {
			continue
		}
		v1 := findHeaderCaseInsensitive(headers1, h)
		v2 := findHeaderCaseInsensitive(headers2, h)

		diff := headerDiff{Name: h}
		if v1 != "" && v2 == "" {
			diff.Status = "only_in_1"
			diff.Value1 = v1
		} else if v1 == "" && v2 != "" {
			diff.Status = "only_in_2"
			diff.Value2 = v2
		} else if v1 == v2 {
			diff.Status = "match"
			diff.Value1 = v1
		} else {
			diff.Status = "differ"
			diff.Value1 = v1
			diff.Value2 = v2
		}
		headerDiffs = append(headerDiffs, diff)
	}
	sort.Slice(headerDiffs, func(i, j int) bool {
		return headerDiffs[i].Name < headerDiffs[j].Name
	})

	// Body structural comparison
	bodyDiff := structuralBodyDiff(body1, body2)

	return mcpJSONResult(map[string]any{
		"statusLine": statusDiff,
		"headers":    headerDiffs,
		"body":       bodyDiff,
	})
}

func findHeaderCaseInsensitive(headers map[string]string, name string) string {
	for k, v := range headers {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

func structuralBodyDiff(body1, body2 string) map[string]any {
	result := map[string]any{
		"match":       body1 == body2,
		"length1":     len(body1),
		"length2":     len(body2),
		"lengthDelta": len(body2) - len(body1),
	}

	// Try JSON structural diff
	var j1, j2 any
	if json.Unmarshal([]byte(body1), &j1) == nil && json.Unmarshal([]byte(body2), &j2) == nil {
		diffs := jsonDiffWalk("$", j1, j2)
		result["type"] = "json"
		result["jsonDiffs"] = diffs
		result["jsonDiffCount"] = len(diffs)
		return result
	}

	// Line-based diff for HTML/text
	lines1 := strings.Split(body1, "\n")
	lines2 := strings.Split(body2, "\n")

	added := 0
	removed := 0
	set1 := make(map[string]int)
	for _, l := range lines1 {
		set1[l]++
	}
	set2 := make(map[string]int)
	for _, l := range lines2 {
		set2[l]++
	}
	for l, c := range set1 {
		if c2, ok := set2[l]; !ok || c2 < c {
			removed += c - set2[l]
		}
	}
	for l, c := range set2 {
		if c1, ok := set1[l]; !ok || c1 < c {
			added += c - set1[l]
		}
	}

	result["type"] = "text"
	result["lines1"] = len(lines1)
	result["lines2"] = len(lines2)
	result["linesAdded"] = added
	result["linesRemoved"] = removed

	return result
}

// ---------------------------------------------------------------------------
// JSON diff: walk both JSON trees, report differences at each path
// ---------------------------------------------------------------------------

type jsonDiffEntry struct {
	Path   string `json:"path"`
	Type   string `json:"type"` // "added", "removed", "changed", "type_mismatch"
	Value1 any    `json:"value1,omitempty"`
	Value2 any    `json:"value2,omitempty"`
}

func jsonDiffWalk(path string, v1, v2 any) []jsonDiffEntry {
	var diffs []jsonDiffEntry

	if v1 == nil && v2 == nil {
		return nil
	}
	if v1 == nil {
		return []jsonDiffEntry{{Path: path, Type: "added", Value2: v2}}
	}
	if v2 == nil {
		return []jsonDiffEntry{{Path: path, Type: "removed", Value1: v1}}
	}

	switch t1 := v1.(type) {
	case map[string]any:
		t2, ok := v2.(map[string]any)
		if !ok {
			return []jsonDiffEntry{{Path: path, Type: "type_mismatch", Value1: v1, Value2: v2}}
		}
		allKeys := make(map[string]bool)
		for k := range t1 {
			allKeys[k] = true
		}
		for k := range t2 {
			allKeys[k] = true
		}
		for k := range allKeys {
			childPath := path + "." + k
			cv1, has1 := t1[k]
			cv2, has2 := t2[k]
			if has1 && !has2 {
				diffs = append(diffs, jsonDiffEntry{Path: childPath, Type: "removed", Value1: cv1})
			} else if !has1 && has2 {
				diffs = append(diffs, jsonDiffEntry{Path: childPath, Type: "added", Value2: cv2})
			} else {
				diffs = append(diffs, jsonDiffWalk(childPath, cv1, cv2)...)
			}
		}

	case []any:
		t2, ok := v2.([]any)
		if !ok {
			return []jsonDiffEntry{{Path: path, Type: "type_mismatch", Value1: v1, Value2: v2}}
		}
		maxLen := len(t1)
		if len(t2) > maxLen {
			maxLen = len(t2)
		}
		for i := 0; i < maxLen; i++ {
			childPath := fmt.Sprintf("%s[%d]", path, i)
			if i >= len(t1) {
				diffs = append(diffs, jsonDiffEntry{Path: childPath, Type: "added", Value2: t2[i]})
			} else if i >= len(t2) {
				diffs = append(diffs, jsonDiffEntry{Path: childPath, Type: "removed", Value1: t1[i]})
			} else {
				diffs = append(diffs, jsonDiffWalk(childPath, t1[i], t2[i])...)
			}
		}

	default:
		// Scalar comparison
		if fmt.Sprintf("%v", v1) != fmt.Sprintf("%v", v2) {
			diffs = append(diffs, jsonDiffEntry{Path: path, Type: "changed", Value1: v1, Value2: v2})
		}
	}

	return diffs
}

// jsonDiffHandler provides standalone JSON diff between two JSON strings.
func (backend *Backend) jsonDiffHandler(ctx context.Context, json1, json2 string) (*mcp.CallToolResult, error) {
	var v1, v2 any
	if err := json.Unmarshal([]byte(json1), &v1); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid JSON in first input: %v", err)), nil
	}
	if err := json.Unmarshal([]byte(json2), &v2); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid JSON in second input: %v", err)), nil
	}

	diffs := jsonDiffWalk("$", v1, v2)
	return mcpJSONResult(map[string]any{
		"diffs": diffs,
		"count": len(diffs),
	})
}
