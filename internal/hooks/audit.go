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
// The file is created lazily on the first event to avoid empty audit files.
type AuditHook struct {
	mu   sync.Mutex
	path string
	file *os.File
}

// NewAuditHook creates an audit hook that will append to the given file path.
// The file is created lazily on the first Handle call.
func NewAuditHook(path string) (*AuditHook, error) {
	return &AuditHook{path: path}, nil
}

// ensureOpen opens the audit file if not already open. Must be called with mu held.
func (h *AuditHook) ensureOpen() error {
	if h.file != nil {
		return nil
	}
	f, err := os.OpenFile(h.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	h.file = f
	return nil
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
	if err := h.ensureOpen(); err != nil {
		return err
	}
	_, err = h.file.Write(data)
	return err
}

// Close closes the underlying file. Safe to call if no events were written.
func (h *AuditHook) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.file != nil {
		return h.file.Close()
	}
	return nil
}
