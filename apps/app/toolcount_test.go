package app

import (
	"os"
	"strings"
	"testing"
)

// expectedToolCount is the number of tools registered via s.AddTool( in
// mcp.go. It is asserted against the source so accidental additions or
// removals of tool registrations are caught in review rather than at runtime.
const expectedToolCount = 44

func TestRegisteredToolCountMatchesConstant(t *testing.T) {
	src, err := os.ReadFile("mcp.go")
	if err != nil {
		t.Fatalf("read mcp.go: %v", err)
	}

	got := strings.Count(string(src), "s.AddTool(")
	if got != expectedToolCount {
		t.Fatalf("registered tool count drifted: mcp.go has %d s.AddTool( calls, expected %d; update expectedToolCount and any tool documentation to match", got, expectedToolCount)
	}
}
