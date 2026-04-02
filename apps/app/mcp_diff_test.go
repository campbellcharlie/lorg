package app

import (
	"encoding/json"
	"testing"
)

func TestJsonDiffWalkIdentical(t *testing.T) {
	var v1, v2 any
	json.Unmarshal([]byte(`{"a":1}`), &v1)
	json.Unmarshal([]byte(`{"a":1}`), &v2)

	diffs := jsonDiffWalk("$", v1, v2)
	if len(diffs) != 0 {
		t.Errorf("expected 0 diffs for identical JSON, got %d: %+v", len(diffs), diffs)
	}
}

func TestJsonDiffWalk(t *testing.T) {
	tests := []struct {
		name      string
		json1     string
		json2     string
		wantCount int
	}{
		{"identical objects", `{"a":1}`, `{"a":1}`, 0},
		{"value changed", `{"a":1}`, `{"a":2}`, 1},
		{"key added", `{"a":1}`, `{"a":1,"b":2}`, 1},
		{"key removed", `{"a":1,"b":2}`, `{"a":1}`, 1},
		{"nested change", `{"a":{"b":1}}`, `{"a":{"b":2}}`, 1},
		{"array element added", `[1,2]`, `[1,2,3]`, 1},
		{"array element removed", `[1,2,3]`, `[1,2]`, 1},
		{"array element changed", `[1,2,3]`, `[1,2,99]`, 1},
		{"type mismatch obj vs array", `{"a":1}`, `[1]`, 1},
		// JSON numbers are float64; {"a":1} vs {"a":"string"} -> scalar comparison
		// fmt.Sprintf("%v",1) = "1" vs fmt.Sprintf("%v","string") = "string" -> changed
		{"scalar type difference", `{"a":1}`, `{"a":"string"}`, 1},
		{"both null", `null`, `null`, 0},
		{"null vs value", `null`, `{"a":1}`, 1},
		{"deeply nested", `{"a":{"b":{"c":1}}}`, `{"a":{"b":{"c":2}}}`, 1},
		{"multiple diffs", `{"a":1,"b":2}`, `{"a":9,"b":8}`, 2},
		{"identical arrays", `[1,2,3]`, `[1,2,3]`, 0},
		{"empty objects", `{}`, `{}`, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v1, v2 any
			if err := json.Unmarshal([]byte(tt.json1), &v1); err != nil {
				t.Fatalf("failed to unmarshal json1: %v", err)
			}
			if err := json.Unmarshal([]byte(tt.json2), &v2); err != nil {
				t.Fatalf("failed to unmarshal json2: %v", err)
			}

			diffs := jsonDiffWalk("$", v1, v2)
			if len(diffs) != tt.wantCount {
				t.Errorf("got %d diffs, want %d. Diffs: %+v", len(diffs), tt.wantCount, diffs)
			}
		})
	}
}

func TestJsonDiffWalkDiffTypes(t *testing.T) {
	// Verify the Type field in diff entries
	var v1, v2 any

	// Key added
	json.Unmarshal([]byte(`{"a":1}`), &v1)
	json.Unmarshal([]byte(`{"a":1,"b":2}`), &v2)
	diffs := jsonDiffWalk("$", v1, v2)
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d", len(diffs))
	}
	if diffs[0].Type != "added" {
		t.Errorf("expected type 'added', got %q", diffs[0].Type)
	}
	if diffs[0].Path != "$.b" {
		t.Errorf("expected path '$.b', got %q", diffs[0].Path)
	}

	// Key removed
	json.Unmarshal([]byte(`{"a":1,"b":2}`), &v1)
	json.Unmarshal([]byte(`{"a":1}`), &v2)
	diffs = jsonDiffWalk("$", v1, v2)
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d", len(diffs))
	}
	if diffs[0].Type != "removed" {
		t.Errorf("expected type 'removed', got %q", diffs[0].Type)
	}

	// Value changed
	json.Unmarshal([]byte(`{"a":1}`), &v1)
	json.Unmarshal([]byte(`{"a":2}`), &v2)
	diffs = jsonDiffWalk("$", v1, v2)
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d", len(diffs))
	}
	if diffs[0].Type != "changed" {
		t.Errorf("expected type 'changed', got %q", diffs[0].Type)
	}
}

func TestJsonDiffWalkNils(t *testing.T) {
	// Both nil
	diffs := jsonDiffWalk("$", nil, nil)
	if len(diffs) != 0 {
		t.Errorf("expected 0 diffs for nil/nil, got %d", len(diffs))
	}

	// v1 nil, v2 non-nil
	diffs = jsonDiffWalk("$", nil, "hello")
	if len(diffs) != 1 || diffs[0].Type != "added" {
		t.Errorf("expected 1 'added' diff, got %+v", diffs)
	}

	// v1 non-nil, v2 nil
	diffs = jsonDiffWalk("$", "hello", nil)
	if len(diffs) != 1 || diffs[0].Type != "removed" {
		t.Errorf("expected 1 'removed' diff, got %+v", diffs)
	}
}

func TestStructuralBodyDiffJSON(t *testing.T) {
	body1 := `{"name":"alice","age":30}`
	body2 := `{"name":"bob","age":30}`

	result := structuralBodyDiff(body1, body2)

	if result["match"] != false {
		t.Error("expected match=false for different bodies")
	}
	if result["type"] != "json" {
		t.Errorf("expected type 'json', got %v", result["type"])
	}
	count, ok := result["jsonDiffCount"].(int)
	if !ok {
		t.Fatalf("jsonDiffCount not an int: %T", result["jsonDiffCount"])
	}
	if count != 1 {
		t.Errorf("expected 1 JSON diff, got %d", count)
	}
}

func TestStructuralBodyDiffText(t *testing.T) {
	body1 := "line1\nline2\nline3"
	body2 := "line1\nline2\nline4"

	result := structuralBodyDiff(body1, body2)

	if result["match"] != false {
		t.Error("expected match=false")
	}
	if result["type"] != "text" {
		t.Errorf("expected type 'text', got %v", result["type"])
	}
}

func TestStructuralBodyDiffIdentical(t *testing.T) {
	body := `{"x":1}`
	result := structuralBodyDiff(body, body)
	if result["match"] != true {
		t.Error("expected match=true for identical bodies")
	}
}
