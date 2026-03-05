package tape

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/joechenrh/golem/internal/llm"
)

func newTestStore(t *testing.T) *FileStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.jsonl")
	s, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore() error: %v", err)
	}
	return s
}

func TestAppendAndEntries(t *testing.T) {
	s := newTestStore(t)

	entry := TapeEntry{
		Kind: KindMessage,
		Payload: map[string]any{
			"role":    "user",
			"content": "hello",
		},
	}
	if err := s.Append(entry); err != nil {
		t.Fatalf("Append() error: %v", err)
	}

	entries, err := s.Entries()
	if err != nil {
		t.Fatalf("Entries() error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}

	got := entries[0]
	if got.Kind != KindMessage {
		t.Errorf("Kind = %q, want %q", got.Kind, KindMessage)
	}
	if got.ID == "" {
		t.Error("ID should be auto-generated")
	}
	if got.Timestamp.IsZero() {
		t.Error("Timestamp should be auto-set")
	}
	if got.Payload["content"] != "hello" {
		t.Errorf("Payload[content] = %v, want %q", got.Payload["content"], "hello")
	}
}

func TestJSONRoundTrip(t *testing.T) {
	s := newTestStore(t)

	now := time.Now().Truncate(time.Millisecond)
	entry := TapeEntry{
		ID:   "test-id-123",
		Kind: KindMessage,
		Payload: map[string]any{
			"role":    "assistant",
			"content": "hi there",
		},
		Timestamp: now,
	}
	if err := s.Append(entry); err != nil {
		t.Fatalf("Append() error: %v", err)
	}

	// Read raw file and verify JSON format.
	data, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}

	var parsed TapeEntry
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if parsed.ID != "test-id-123" {
		t.Errorf("ID = %q, want %q", parsed.ID, "test-id-123")
	}
	if parsed.Kind != KindMessage {
		t.Errorf("Kind = %q, want %q", parsed.Kind, KindMessage)
	}
	if parsed.Payload["role"] != "assistant" {
		t.Errorf("Payload[role] = %v, want %q", parsed.Payload["role"], "assistant")
	}
}

func TestSearch(t *testing.T) {
	s := newTestStore(t)

	entries := []TapeEntry{
		{Kind: KindMessage, Payload: map[string]any{"content": "Hello World"}},
		{Kind: KindMessage, Payload: map[string]any{"content": "Goodbye"}},
		{Kind: KindMessage, Payload: map[string]any{"content": "hello again"}},
	}
	for _, e := range entries {
		if err := s.Append(e); err != nil {
			t.Fatalf("Append() error: %v", err)
		}
	}

	results, err := s.Search("hello")
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
}

func TestAddAnchorAndEntriesSince(t *testing.T) {
	s := newTestStore(t)

	// Add some messages.
	s.Append(TapeEntry{Kind: KindMessage, Payload: map[string]any{"content": "before"}})

	// Add anchor.
	if err := s.AddAnchor("context-reset"); err != nil {
		t.Fatalf("AddAnchor() error: %v", err)
	}

	// Add messages after anchor.
	s.Append(TapeEntry{Kind: KindMessage, Payload: map[string]any{"content": "after1"}})
	s.Append(TapeEntry{Kind: KindMessage, Payload: map[string]any{"content": "after2"}})

	// Find anchor.
	anchor, err := s.LastAnchor()
	if err != nil {
		t.Fatalf("LastAnchor() error: %v", err)
	}
	if anchor == nil {
		t.Fatal("LastAnchor() returned nil")
	}
	if anchor.Kind != KindAnchor {
		t.Errorf("anchor Kind = %q, want %q", anchor.Kind, KindAnchor)
	}

	// Get entries since anchor.
	after, err := s.EntriesSince(anchor.ID)
	if err != nil {
		t.Fatalf("EntriesSince() error: %v", err)
	}
	if len(after) != 2 {
		t.Fatalf("len(after) = %d, want 2", len(after))
	}
	if after[0].Payload["content"] != "after1" {
		t.Errorf("first entry content = %v, want %q", after[0].Payload["content"], "after1")
	}
}

func TestEntriesSince_NotFound(t *testing.T) {
	s := newTestStore(t)

	_, err := s.EntriesSince("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent anchor")
	}
}

func TestLastAnchor_None(t *testing.T) {
	s := newTestStore(t)

	s.Append(TapeEntry{Kind: KindMessage, Payload: map[string]any{"content": "msg"}})

	anchor, err := s.LastAnchor()
	if err != nil {
		t.Fatalf("LastAnchor() error: %v", err)
	}
	if anchor != nil {
		t.Errorf("expected nil anchor, got %+v", anchor)
	}
}

func TestInfo(t *testing.T) {
	s := newTestStore(t)

	s.Append(TapeEntry{Kind: KindMessage, Payload: map[string]any{"content": "msg1"}})
	s.Append(TapeEntry{Kind: KindMessage, Payload: map[string]any{"content": "msg2"}})
	s.AddAnchor("test-anchor")
	s.Append(TapeEntry{Kind: KindMessage, Payload: map[string]any{"content": "msg3"}})

	info := s.Info()
	if info.TotalEntries != 4 {
		t.Errorf("TotalEntries = %d, want 4", info.TotalEntries)
	}
	if info.AnchorCount != 1 {
		t.Errorf("AnchorCount = %d, want 1", info.AnchorCount)
	}
	if info.EntriesSinceAnchor != 1 {
		t.Errorf("EntriesSinceAnchor = %d, want 1", info.EntriesSinceAnchor)
	}
	if info.FilePath != s.path {
		t.Errorf("FilePath = %q, want %q", info.FilePath, s.path)
	}
}

func TestBuildMessages(t *testing.T) {
	entries := []TapeEntry{
		{Kind: KindMessage, Payload: map[string]any{"role": "user", "content": "before anchor"}},
		{Kind: KindAnchor, Payload: map[string]any{"label": "reset"}},
		{Kind: KindMessage, Payload: map[string]any{"role": "user", "content": "hello"}},
		{Kind: KindEvent, Payload: map[string]any{"type": "command"}},
		{Kind: KindMessage, Payload: map[string]any{"role": "assistant", "content": "hi there"}},
	}

	msgs := BuildMessages(entries)

	if len(msgs) != 2 {
		t.Fatalf("len(msgs) = %d, want 2", len(msgs))
	}
	if msgs[0].Role != llm.RoleUser || msgs[0].Content != "hello" {
		t.Errorf("msgs[0] = %+v", msgs[0])
	}
	if msgs[1].Role != llm.RoleAssistant || msgs[1].Content != "hi there" {
		t.Errorf("msgs[1] = %+v", msgs[1])
	}
}

func TestBuildMessages_NoAnchor(t *testing.T) {
	entries := []TapeEntry{
		{Kind: KindMessage, Payload: map[string]any{"role": "user", "content": "hello"}},
		{Kind: KindMessage, Payload: map[string]any{"role": "assistant", "content": "hi"}},
	}

	msgs := BuildMessages(entries)
	if len(msgs) != 2 {
		t.Fatalf("len(msgs) = %d, want 2", len(msgs))
	}
}

func TestBuildMessages_WithToolCalls(t *testing.T) {
	entries := []TapeEntry{
		{Kind: KindMessage, Payload: map[string]any{
			"role": "assistant",
			"tool_calls": []any{
				map[string]any{
					"id":        "call_1",
					"name":      "read_file",
					"arguments": `{"path":"/tmp/x.txt"}`,
				},
			},
		}},
		{Kind: KindMessage, Payload: map[string]any{
			"role":         "tool",
			"content":      "file contents",
			"tool_call_id": "call_1",
			"name":         "read_file",
		}},
	}

	msgs := BuildMessages(entries)
	if len(msgs) != 2 {
		t.Fatalf("len(msgs) = %d, want 2", len(msgs))
	}

	if len(msgs[0].ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(msgs[0].ToolCalls))
	}
	if msgs[0].ToolCalls[0].Name != "read_file" {
		t.Errorf("ToolCall.Name = %q, want %q", msgs[0].ToolCalls[0].Name, "read_file")
	}
	if msgs[1].Role != llm.RoleTool || msgs[1].ToolCallID != "call_1" {
		t.Errorf("tool msg = %+v", msgs[1])
	}
}

func TestGracefulRecovery_InvalidJSON(t *testing.T) {
	s := newTestStore(t)

	// Write valid entry then invalid line directly.
	s.Append(TapeEntry{Kind: KindMessage, Payload: map[string]any{"content": "valid"}})

	f, _ := os.OpenFile(s.path, os.O_APPEND|os.O_WRONLY, 0644)
	f.Write([]byte("not valid json\n"))
	f.Close()

	s.Append(TapeEntry{Kind: KindMessage, Payload: map[string]any{"content": "also valid"}})

	entries, err := s.Entries()
	if err != nil {
		t.Fatalf("Entries() error: %v", err)
	}
	// Invalid line should be skipped.
	if len(entries) != 2 {
		t.Errorf("len(entries) = %d, want 2 (invalid line skipped)", len(entries))
	}
}

func TestSessionRestore_LoadFromDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restore.jsonl")

	// Create a store and write some entries.
	s1, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore() error: %v", err)
	}
	s1.Append(TapeEntry{Kind: KindMessage, Payload: map[string]any{"content": "msg1"}})
	s1.Append(TapeEntry{Kind: KindMessage, Payload: map[string]any{"content": "msg2"}})

	// Open a new store from the same file — simulates session restore.
	s2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore() error: %v", err)
	}

	entries, err := s2.Entries()
	if err != nil {
		t.Fatalf("Entries() error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if entries[0].Payload["content"] != "msg1" {
		t.Errorf("entries[0] content = %v, want msg1", entries[0].Payload["content"])
	}

	// Append to the restored store and verify.
	s2.Append(TapeEntry{Kind: KindMessage, Payload: map[string]any{"content": "msg3"}})
	entries, _ = s2.Entries()
	if len(entries) != 3 {
		t.Fatalf("len(entries) after append = %d, want 3", len(entries))
	}
}
