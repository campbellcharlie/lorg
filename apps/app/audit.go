package app

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AuditLogger provides tamper-evident JSONL logging with SHA-256 hash chaining.
// Each entry includes a hash of its contents plus the previous entry's hash,
// forming a verifiable chain. An empty dir disables logging.
type AuditLogger struct {
	mu       sync.Mutex
	file     *os.File
	path     string
	prevHash string
	enabled  bool
}

type auditEntry struct {
	Timestamp string `json:"timestamp"`
	Action    string `json:"action"`
	Details   any    `json:"details,omitempty"`
	PrevHash  string `json:"prev_hash"`
	Hash      string `json:"hash"`
}

// NewAuditLogger creates a new audit logger that writes to the given directory.
// The file is named audit-YYYY-MM-DD.jsonl. If dir is empty, logging is disabled.
func NewAuditLogger(dir string) (*AuditLogger, error) {
	if dir == "" {
		return &AuditLogger{enabled: false}, nil
	}

	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create audit dir: %w", err)
	}

	filename := fmt.Sprintf("audit-%s.jsonl", time.Now().Format("2006-01-02"))
	path := filepath.Join(dir, filename)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to open audit log: %w", err)
	}

	al := &AuditLogger{
		file:     f,
		path:     path,
		prevHash: "0000000000000000000000000000000000000000000000000000000000000000",
		enabled:  true,
	}

	// Resume chain from last entry if the file already has content.
	al.loadLastHash()

	log.Printf("[Audit] Logging to %s", path)
	return al, nil
}

// loadLastHash reads the audit file to find the most recent entry's hash,
// allowing the chain to resume across restarts.
func (al *AuditLogger) loadLastHash() {
	if al.file == nil {
		return
	}
	data, err := os.ReadFile(al.path)
	if err != nil || len(data) == 0 {
		return
	}
	lines := splitLines(string(data))
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if line == "" {
			continue
		}
		var entry auditEntry
		if err := json.Unmarshal([]byte(line), &entry); err == nil && entry.Hash != "" {
			al.prevHash = entry.Hash
			return
		}
	}
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// Log writes an audit entry with hash chaining. It is safe for concurrent use.
func (al *AuditLogger) Log(action string, details any) {
	if !al.enabled || al.file == nil {
		return
	}

	al.mu.Lock()
	defer al.mu.Unlock()

	entry := auditEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Action:    action,
		Details:   details,
		PrevHash:  al.prevHash,
	}

	// Compute hash over entry fields (excluding the hash field itself).
	preHash, _ := json.Marshal(map[string]any{
		"timestamp": entry.Timestamp,
		"action":    entry.Action,
		"details":   entry.Details,
		"prev_hash": entry.PrevHash,
	})
	hash := sha256.Sum256(preHash)
	entry.Hash = fmt.Sprintf("%x", hash)
	al.prevHash = entry.Hash

	line, err := json.Marshal(entry)
	if err != nil {
		log.Printf("[Audit] Failed to marshal entry: %v", err)
		return
	}

	if _, err := al.file.Write(append(line, '\n')); err != nil {
		log.Printf("[Audit] Failed to write entry: %v", err)
	}
}

// Close closes the audit log file.
func (al *AuditLogger) Close() error {
	if al.file != nil {
		return al.file.Close()
	}
	return nil
}
