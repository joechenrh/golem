package hooks

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"time"
)

// AuditHook writes structured JSON audit entries to a file.
// Each line is a self-contained JSON object for easy parsing.
type AuditHook struct {
	mu   sync.Mutex
	file *os.File
}

// NewAuditHook creates an audit hook that appends to the given file path.
// The file is created if it does not exist.
func NewAuditHook(path string) (*AuditHook, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &AuditHook{file: f}, nil
}

func (h *AuditHook) Name() string { return "audit" }

// auditEntry is the JSON structure written for each event.
type auditEntry struct {
	Timestamp string         `json:"ts"`
	Event     EventType      `json:"event"`
	Payload   map[string]any `json:"payload,omitempty"`
}

func (h *AuditHook) Handle(_ context.Context, event Event) error {
	entry := auditEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Event:     event.Type,
		Payload:   event.Payload,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err = h.file.Write(data)
	return err
}

// Close closes the underlying file.
func (h *AuditHook) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.file.Close()
}
