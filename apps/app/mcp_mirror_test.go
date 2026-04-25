package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMirror_ReplayVerbatim(t *testing.T) {
	base := "GET /api/users HTTP/1.1\r\nHost: example.com\r\nAccept: */*\r\n\r\n"
	out, summary, err := applyMutations(base, MirrorArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "GET /api/users HTTP/1.1") {
		t.Errorf("verbatim replay should preserve request line, got %q", out)
	}
	if len(summary) != 1 || summary[0] != "none — replayed verbatim" {
		t.Errorf("expected single 'verbatim' summary, got %v", summary)
	}
}

func TestMirror_ReplaceMethod(t *testing.T) {
	base := "GET /x HTTP/1.1\r\nHost: x\r\n\r\n"
	out, summary, _ := applyMutations(base, MirrorArgs{Method: "delete"})
	if !strings.HasPrefix(out, "DELETE /x HTTP/1.1") {
		t.Errorf("expected DELETE method, got %q", out[:30])
	}
	if !contains(summary, "method→DELETE") {
		t.Errorf("summary should mention method change, got %v", summary)
	}
}

func TestMirror_ReplacePath(t *testing.T) {
	base := "GET /old?keep=1 HTTP/1.1\r\nHost: x\r\n\r\n"
	out, _, _ := applyMutations(base, MirrorArgs{Path: "/new"})
	if !strings.HasPrefix(out, "GET /new?keep=1 HTTP/1.1") {
		t.Errorf("path change should preserve query, got %q", out[:40])
	}
}

func TestMirror_AppendQuery(t *testing.T) {
	base := "GET /x?a=1 HTTP/1.1\r\nHost: x\r\n\r\n"
	out, summary, _ := applyMutations(base, MirrorArgs{AppendQuery: map[string]string{"b": "2"}})
	if !strings.Contains(out, "a=1") || !strings.Contains(out, "b=2") {
		t.Errorf("appendQuery should preserve a + add b, got %q", out)
	}
	if !contains(summary, "appendQuery+1") {
		t.Errorf("summary should note appendQuery, got %v", summary)
	}
}

func TestMirror_SetHeader(t *testing.T) {
	base := "POST /x HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer old-token\r\n\r\n"
	out, _, _ := applyMutations(base, MirrorArgs{SetHeaders: map[string]string{"Authorization": "Bearer new-token"}})
	if !strings.Contains(out, "Authorization: Bearer new-token") {
		t.Errorf("Authorization should be replaced, got:\n%s", out)
	}
	if strings.Contains(out, "old-token") {
		t.Errorf("old token should be gone, got:\n%s", out)
	}
	// Case-insensitive name match — set header AUTHORIZATION should still hit
	out2, _, _ := applyMutations(base, MirrorArgs{SetHeaders: map[string]string{"AUTHORIZATION": "Bearer x"}})
	if strings.Count(out2, "Bearer") != 1 {
		t.Errorf("case-insensitive set should replace, not duplicate, got:\n%s", out2)
	}
}

func TestMirror_AppendNewHeader(t *testing.T) {
	base := "GET /x HTTP/1.1\r\nHost: x\r\n\r\n"
	out, _, _ := applyMutations(base, MirrorArgs{SetHeaders: map[string]string{"X-Custom": "yes"}})
	if !strings.Contains(out, "X-Custom: yes") {
		t.Errorf("new header should be appended, got:\n%s", out)
	}
}

func TestMirror_RemoveHeader(t *testing.T) {
	base := "GET /x HTTP/1.1\r\nHost: x\r\nCookie: session=abc\r\nAccept: */*\r\n\r\n"
	out, summary, _ := applyMutations(base, MirrorArgs{RemoveHeaders: []string{"cookie"}})
	if strings.Contains(out, "Cookie") {
		t.Errorf("Cookie should be removed, got:\n%s", out)
	}
	if !strings.Contains(out, "Accept") {
		t.Errorf("Accept should remain, got:\n%s", out)
	}
	if !contains(summary, "removeHeaders-1") {
		t.Errorf("summary should note remove, got %v", summary)
	}
}

func TestMirror_ReplaceBody_JSON(t *testing.T) {
	base := "POST /x HTTP/1.1\r\nHost: x\r\nContent-Type: text/plain\r\nContent-Length: 5\r\n\r\nhello"
	bodyJSON := json.RawMessage(`{"key":"value"}`)
	out, summary, _ := applyMutations(base, MirrorArgs{Body: bodyJSON})
	if !strings.HasSuffix(out, `{"key":"value"}`) {
		t.Errorf("body should be replaced with JSON, got:\n%s", out)
	}
	if !strings.Contains(out, "Content-Type: application/json") {
		t.Errorf("Content-Type should auto-flip to application/json, got:\n%s", out)
	}
	if !strings.Contains(out, "Content-Length: 15") {
		t.Errorf("Content-Length should match new body (15 bytes), got:\n%s", out)
	}
	if !contains(summary, "body→15B") {
		t.Errorf("summary should note body change, got %v", summary)
	}
}

func TestMirror_ReplaceBody_String(t *testing.T) {
	// JSON string → use literal value, no Content-Type flip
	base := "POST /x HTTP/1.1\r\nHost: x\r\nContent-Type: text/plain\r\nContent-Length: 5\r\n\r\nhello"
	bodyStr := json.RawMessage(`"raw=string&form=data"`)
	out, _, _ := applyMutations(base, MirrorArgs{Body: bodyStr})
	if !strings.HasSuffix(out, "raw=string&form=data") {
		t.Errorf("string body should be used verbatim, got:\n%s", out)
	}
	if !strings.Contains(out, "Content-Type: text/plain") {
		t.Errorf("Content-Type should NOT flip for string bodies, got:\n%s", out)
	}
}

func TestMirror_TruncateBody(t *testing.T) {
	body := strings.Repeat("X", 10000)
	out, truncated := truncateBody(body, 100)
	if !truncated {
		t.Error("body should be reported truncated")
	}
	if len(out) >= len(body) {
		t.Error("truncated body should be shorter than input")
	}
	if !strings.Contains(out, "[truncated") {
		t.Error("truncation marker should be present")
	}
	full, trunc2 := truncateBody(body, 0)
	if trunc2 || full != body {
		t.Error("maxBytes=0 should disable truncation")
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func TestMirror_BatchMerge_EntryWinsOverSingleton(t *testing.T) {
	base := MirrorArgs{
		Method:     "POST",
		Path:       "/api/old",
		SetHeaders: map[string]string{"Authorization": "Bearer X", "X-A": "1"},
	}
	entry := MirrorBatchEntry{
		Path:       "/api/new",
		SetHeaders: map[string]string{"X-A": "2"}, // override single key
	}
	merged := mergeMirrorEntry(base, entry)

	if merged.Method != "POST" {
		t.Errorf("entry method empty → singleton wins, got %q", merged.Method)
	}
	if merged.Path != "/api/new" {
		t.Errorf("entry path should win, got %q", merged.Path)
	}
	if merged.SetHeaders["Authorization"] != "Bearer X" {
		t.Errorf("singleton-only header missing, got %v", merged.SetHeaders)
	}
	if merged.SetHeaders["X-A"] != "2" {
		t.Errorf("entry header should override singleton, got %v", merged.SetHeaders)
	}
	if merged.Batch != nil {
		t.Errorf("merged single-iteration view shouldn't carry batch")
	}
}

func TestMirror_BatchMerge_EmptyEntryInheritsAll(t *testing.T) {
	base := MirrorArgs{
		Method: "DELETE",
		Path:   "/api/x",
		Note:   "from-singleton",
	}
	merged := mergeMirrorEntry(base, MirrorBatchEntry{})

	if merged.Method != "DELETE" || merged.Path != "/api/x" || merged.Note != "from-singleton" {
		t.Errorf("empty entry should inherit all singleton fields, got %+v", merged)
	}
}

func TestMirror_BatchMerge_AppendQueryAndRemoveHeadersAreUnioned(t *testing.T) {
	base := MirrorArgs{
		AppendQuery:   map[string]string{"a": "1"},
		RemoveHeaders: []string{"Cookie"},
	}
	entry := MirrorBatchEntry{
		AppendQuery:   map[string]string{"b": "2"},
		RemoveHeaders: []string{"Authorization"},
	}
	merged := mergeMirrorEntry(base, entry)

	if merged.AppendQuery["a"] != "1" || merged.AppendQuery["b"] != "2" {
		t.Errorf("appendQuery should union both, got %v", merged.AppendQuery)
	}
	if !contains(merged.RemoveHeaders, "Cookie") || !contains(merged.RemoveHeaders, "Authorization") {
		t.Errorf("removeHeaders should concat both, got %v", merged.RemoveHeaders)
	}
}
