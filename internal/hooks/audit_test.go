package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditHook_WritesJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")

	h, err := NewAuditHook(path)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	if h.Name() != "audit" {
		t.Errorf("name = %q, want %q", h.Name(), "audit")
	}

	// Emit a few events.
	events := []Event{
		{Type: EventUserMessage, Payload: map[string]any{"text": "hello"}},
		{Type: EventBeforeToolExec, Payload: map[string]any{"tool_name": "shell_exec", "arguments": `{"command":"ls"}`}},
		{Type: EventAfterToolExec, Payload: map[string]any{"tool_name": "shell_exec", "result": "file.go"}},
	}
	for _, e := range events {
		if err := h.Handle(context.Background(), e); err != nil {
			t.Fatalf("Handle(%s): %v", e.Type, err)
		}
	}

	// Read and verify.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}

	// Verify first entry.
	var entry auditEntry
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("invalid JSON on line 1: %v", err)
	}
	if entry.Event != EventUserMessage {
		t.Errorf("event = %q, want %q", entry.Event, EventUserMessage)
	}
	if entry.Timestamp == "" {
		t.Error("expected non-empty timestamp")
	}
	if entry.Payload["text"] != "hello" {
		t.Errorf("payload text = %v, want %q", entry.Payload["text"], "hello")
	}

	// Verify second entry has tool info.
	var entry2 auditEntry
	if err := json.Unmarshal([]byte(lines[1]), &entry2); err != nil {
		t.Fatalf("invalid JSON on line 2: %v", err)
	}
	if entry2.Event != EventBeforeToolExec {
		t.Errorf("event = %q, want %q", entry2.Event, EventBeforeToolExec)
	}
	if entry2.Payload["tool_name"] != "shell_exec" {
		t.Errorf("tool_name = %v, want %q", entry2.Payload["tool_name"], "shell_exec")
	}
}

func TestAuditHook_NilPayload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")

	h, err := NewAuditHook(path)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	err = h.Handle(context.Background(), Event{Type: EventError})
	if err != nil {
		t.Fatalf("nil payload should not error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var entry auditEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &entry); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if entry.Event != EventError {
		t.Errorf("event = %q, want %q", entry.Event, EventError)
	}
}

func TestAuditHook_AppendsToExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")

	// Write first entry.
	h1, err := NewAuditHook(path)
	if err != nil {
		t.Fatal(err)
	}
	h1.Handle(context.Background(), Event{Type: EventUserMessage, Payload: map[string]any{"n": 1}})
	h1.Close()

	// Reopen and write second entry.
	h2, err := NewAuditHook(path)
	if err != nil {
		t.Fatal(err)
	}
	h2.Handle(context.Background(), Event{Type: EventUserMessage, Payload: map[string]any{"n": 2}})
	h2.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines after reopen, got %d", len(lines))
	}
}

func TestAuditHook_InvalidPath(t *testing.T) {
	_, err := NewAuditHook("/nonexistent/dir/audit.jsonl")
	if err == nil {
		t.Error("expected error for invalid path")
	}
}
