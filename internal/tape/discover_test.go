package tape

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAgentDir(t *testing.T) {
	base := t.TempDir()

	dir, err := AgentDir(base, "myagent")
	if err != nil {
		t.Fatalf("AgentDir() error: %v", err)
	}

	want := filepath.Join(base, "myagent")
	if dir != want {
		t.Errorf("AgentDir() = %q, want %q", dir, want)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat(%q) error: %v", dir, err)
	}
	if !info.IsDir() {
		t.Errorf("%q is not a directory", dir)
	}
}

func TestAgentDir_Idempotent(t *testing.T) {
	base := t.TempDir()

	dir1, err := AgentDir(base, "agent")
	if err != nil {
		t.Fatalf("first AgentDir() error: %v", err)
	}

	dir2, err := AgentDir(base, "agent")
	if err != nil {
		t.Fatalf("second AgentDir() error: %v", err)
	}

	if dir1 != dir2 {
		t.Errorf("AgentDir() not idempotent: %q != %q", dir1, dir2)
	}
}

func TestExtractLastSummary(t *testing.T) {
	t.Run("returns last summary", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.jsonl")

		// Write a tape with two summaries — should return the last one.
		entries := []TapeEntry{
			{ID: "1", Kind: KindMessage, Payload: MarshalPayload(map[string]string{"role": "user", "content": "hello"}), Timestamp: time.Now()},
			{ID: "2", Kind: KindSummary, Payload: MarshalPayload(map[string]string{"summary": "first summary"}), Timestamp: time.Now()},
			{ID: "3", Kind: KindMessage, Payload: MarshalPayload(map[string]string{"role": "user", "content": "world"}), Timestamp: time.Now()},
			{ID: "4", Kind: KindSummary, Payload: MarshalPayload(map[string]string{"summary": "second summary"}), Timestamp: time.Now()},
		}

		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			data, _ := json.Marshal(e)
			f.Write(data)
			f.Write([]byte("\n"))
		}
		f.Close()

		got := ExtractLastSummary(path)
		if got != "second summary" {
			t.Errorf("ExtractLastSummary() = %q, want %q", got, "second summary")
		}
	})

	t.Run("returns empty for no summary", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.jsonl")

		entries := []TapeEntry{
			{ID: "1", Kind: KindMessage, Payload: MarshalPayload(map[string]string{"role": "user", "content": "hello"}), Timestamp: time.Now()},
		}

		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			data, _ := json.Marshal(e)
			f.Write(data)
			f.Write([]byte("\n"))
		}
		f.Close()

		got := ExtractLastSummary(path)
		if got != "" {
			t.Errorf("ExtractLastSummary() = %q, want empty", got)
		}
	})

	t.Run("returns empty for missing file", func(t *testing.T) {
		got := ExtractLastSummary("/nonexistent/file.jsonl")
		if got != "" {
			t.Errorf("ExtractLastSummary() = %q, want empty", got)
		}
	})
}
