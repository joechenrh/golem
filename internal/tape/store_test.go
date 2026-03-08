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

func TestFileStore_NoFileWhenUnused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lazy.jsonl")
	s, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore() error: %v", err)
	}
	s.Close()

	// File should not exist since nothing was written.
	if _, err := os.Stat(path); err == nil {
		t.Error("tape file should not exist when no entries were appended")
	}
}

func TestAppendAndEntries(t *testing.T) {
	s := newTestStore(t)

	entry := TapeEntry{
		Kind: KindMessage,
		Payload: MarshalPayload(map[string]any{
			"role":    "user",
			"content": "hello",
		}),
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
	if p := got.PayloadMap(); p["content"] != "hello" {
		t.Errorf("Payload[content] = %v, want %q", p["content"], "hello")
	}
}

func TestJSONRoundTrip(t *testing.T) {
	s := newTestStore(t)

	now := time.Now().Truncate(time.Millisecond)
	entry := TapeEntry{
		ID:   "test-id-123",
		Kind: KindMessage,
		Payload: MarshalPayload(map[string]any{
			"role":    "assistant",
			"content": "hi there",
		}),
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
	if p := parsed.PayloadMap(); p["role"] != "assistant" {
		t.Errorf("Payload[role] = %v, want %q", p["role"], "assistant")
	}
}

func TestSearch(t *testing.T) {
	s := newTestStore(t)

	entries := []TapeEntry{
		{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "Hello World"})},
		{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "Goodbye"})},
		{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "hello again"})},
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
	s.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "before"})})

	// Add anchor.
	if err := s.AddAnchor("context-reset"); err != nil {
		t.Fatalf("AddAnchor() error: %v", err)
	}

	// Add messages after anchor.
	s.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "after1"})})
	s.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "after2"})})

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
	if p := after[0].PayloadMap(); p["content"] != "after1" {
		t.Errorf("first entry content = %v, want %q", p["content"], "after1")
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

	s.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "msg"})})

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

	s.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "msg1"})})
	s.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "msg2"})})
	s.AddAnchor("test-anchor")
	s.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "msg3"})})

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
		{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"role": "user", "content": "before anchor"})},
		{Kind: KindAnchor, Payload: MarshalPayload(map[string]any{"label": "reset"})},
		{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"role": "user", "content": "hello"})},
		{Kind: KindEvent, Payload: MarshalPayload(map[string]any{"type": "command"})},
		{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"role": "assistant", "content": "hi there"})},
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
		{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"role": "user", "content": "hello"})},
		{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"role": "assistant", "content": "hi"})},
	}

	msgs := BuildMessages(entries)
	if len(msgs) != 2 {
		t.Fatalf("len(msgs) = %d, want 2", len(msgs))
	}
}

func TestBuildMessages_WithToolCalls(t *testing.T) {
	entries := []TapeEntry{
		{Kind: KindMessage, Payload: MarshalPayload(map[string]any{
			"role": "assistant",
			"tool_calls": []any{
				map[string]any{
					"id":        "call_1",
					"name":      "read_file",
					"arguments": `{"path":"/tmp/x.txt"}`,
				},
			},
		})},
		{Kind: KindMessage, Payload: MarshalPayload(map[string]any{
			"role":         "tool",
			"content":      "file contents",
			"tool_call_id": "call_1",
			"name":         "read_file",
		})},
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
	s.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "valid"})})

	f, _ := os.OpenFile(s.path, os.O_APPEND|os.O_WRONLY, 0644)
	f.Write([]byte("not valid json\n"))
	f.Close()

	s.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "also valid"})})

	entries, err := s.Entries()
	if err != nil {
		t.Fatalf("Entries() error: %v", err)
	}
	// Invalid line should be skipped.
	if len(entries) != 2 {
		t.Errorf("len(entries) = %d, want 2 (invalid line skipped)", len(entries))
	}
}

func TestGracefulRecovery_TruncatedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "truncated.jsonl")

	// Write a valid entry, then a truncated JSON line (simulating a crash mid-write).
	validEntry := `{"id":"e1","kind":"message","payload":{"content":"valid"},"timestamp":"2025-01-01T00:00:00Z"}`
	truncatedLine := `{"kind":"message","payl`

	if err := os.WriteFile(path, []byte(validEntry+"\n"+truncatedLine+"\n"), 0644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	s, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore() error: %v", err)
	}
	defer s.Close()

	entries, err := s.Entries()
	if err != nil {
		t.Fatalf("Entries() error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1 (truncated line should be skipped)", len(entries))
	}
	if entries[0].ID != "e1" {
		t.Errorf("entry ID = %q, want %q", entries[0].ID, "e1")
	}
}

func TestGracefulRecovery_MultipleConsecutiveInvalidLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multi-invalid.jsonl")

	valid1 := `{"id":"e1","kind":"message","payload":{"content":"first"},"timestamp":"2025-01-01T00:00:00Z"}`
	invalid1 := `{not json at all}`
	invalid2 := `totally not json`
	invalid3 := `{"kind":"message","truncated`
	valid2 := `{"id":"e2","kind":"message","payload":{"content":"second"},"timestamp":"2025-01-02T00:00:00Z"}`

	content := valid1 + "\n" + invalid1 + "\n" + invalid2 + "\n" + invalid3 + "\n" + valid2 + "\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	s, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore() error: %v", err)
	}
	defer s.Close()

	entries, err := s.Entries()
	if err != nil {
		t.Fatalf("Entries() error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2 (all invalid lines should be skipped)", len(entries))
	}
	if entries[0].ID != "e1" {
		t.Errorf("entries[0].ID = %q, want %q", entries[0].ID, "e1")
	}
	if entries[1].ID != "e2" {
		t.Errorf("entries[1].ID = %q, want %q", entries[1].ID, "e2")
	}
}

func TestGracefulRecovery_MixedValidEmptyAndInvalidLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mixed.jsonl")

	valid1 := `{"id":"e1","kind":"message","payload":{"content":"one"},"timestamp":"2025-01-01T00:00:00Z"}`
	valid2 := `{"id":"e2","kind":"event","payload":{"type":"session_start"},"timestamp":"2025-01-01T00:01:00Z"}`
	valid3 := `{"id":"e3","kind":"message","payload":{"content":"three"},"timestamp":"2025-01-01T00:02:00Z"}`
	invalid := `{"broken":`

	// Construct a file with a mix: valid, empty, invalid, empty, valid, invalid, valid.
	lines := []string{valid1, "", invalid, "", valid2, `{bad json}`, valid3, ""}
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	s, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore() error: %v", err)
	}
	defer s.Close()

	entries, err := s.Entries()
	if err != nil {
		t.Fatalf("Entries() error: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("len(entries) = %d, want 3 (empty and invalid lines should be skipped)", len(entries))
	}
	if entries[0].ID != "e1" {
		t.Errorf("entries[0].ID = %q, want %q", entries[0].ID, "e1")
	}
	if entries[1].ID != "e2" {
		t.Errorf("entries[1].ID = %q, want %q", entries[1].ID, "e2")
	}
	if entries[2].ID != "e3" {
		t.Errorf("entries[2].ID = %q, want %q", entries[2].ID, "e3")
	}

	// Verify the kinds loaded correctly.
	if entries[0].Kind != KindMessage {
		t.Errorf("entries[0].Kind = %q, want %q", entries[0].Kind, KindMessage)
	}
	if entries[1].Kind != KindEvent {
		t.Errorf("entries[1].Kind = %q, want %q", entries[1].Kind, KindEvent)
	}
	if entries[2].Kind != KindMessage {
		t.Errorf("entries[2].Kind = %q, want %q", entries[2].Kind, KindMessage)
	}
}

func TestSessionRestore_LoadFromDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restore.jsonl")

	// Create a store and write some entries.
	s1, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore() error: %v", err)
	}
	s1.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "msg1"})})
	s1.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "msg2"})})

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
	if p := entries[0].PayloadMap(); p["content"] != "msg1" {
		t.Errorf("entries[0] content = %v, want msg1", p["content"])
	}

	// Append to the restored store and verify.
	s2.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "msg3"})})
	entries, _ = s2.Entries()
	if len(entries) != 3 {
		t.Fatalf("len(entries) after append = %d, want 3", len(entries))
	}
}

func TestFileStore_RotationPersistsRetainedEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tape.jsonl")
	s, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore() error: %v", err)
	}

	// Add an anchor and a message after it.
	s.Append(TapeEntry{Kind: KindAnchor, Payload: MarshalPayload(map[string]any{"label": "ctx"})})
	s.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "keep-me"})})

	// Force rotation by setting diskBytes above threshold.
	s.mu.Lock()
	s.diskBytes = MaxTapeFileSize
	s.mu.Unlock()
	// The next Append triggers rotation.
	s.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "after-rotation"})})

	// Close the store (simulates a crash right after rotation).
	s.Close()

	// Reopen — the retained entries (anchor + "keep-me") plus the new
	// entry should all be present on disk.
	s2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("reopen error: %v", err)
	}
	defer s2.Close()

	entries, _ := s2.Entries()
	if len(entries) != 3 {
		t.Fatalf("len(entries) = %d, want 3 (anchor + keep-me + after-rotation)", len(entries))
	}

	// Verify the retained content.
	p := entries[0].PayloadMap()
	if p["label"] != "ctx" {
		t.Errorf("entries[0] label = %v, want ctx", p["label"])
	}
	p = entries[1].PayloadMap()
	if p["content"] != "keep-me" {
		t.Errorf("entries[1] content = %v, want keep-me", p["content"])
	}
	p = entries[2].PayloadMap()
	if p["content"] != "after-rotation" {
		t.Errorf("entries[2] content = %v, want after-rotation", p["content"])
	}
}
