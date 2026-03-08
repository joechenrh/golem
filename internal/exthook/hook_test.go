package exthook

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseHook(t *testing.T) {
	t.Run("valid HOOK.md", func(t *testing.T) {
		h, err := ParseHook("testdata/hooks/memory-loader/HOOK.md")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if h.Name != "memory-loader" {
			t.Errorf("name = %q, want %q", h.Name, "memory-loader")
		}
		if h.Description != "Load relevant memory before LLM calls" {
			t.Errorf("description = %q", h.Description)
		}
		if len(h.Events) != 2 {
			t.Fatalf("events count = %d, want 2", len(h.Events))
		}
		if h.Events[0] != EventBeforeLLMCall {
			t.Errorf("events[0] = %q, want %q", h.Events[0], EventBeforeLLMCall)
		}
		if h.Events[1] != EventAfterReset {
			t.Errorf("events[1] = %q, want %q", h.Events[1], EventAfterReset)
		}
		if h.Timeout != 10*time.Second {
			t.Errorf("timeout = %v, want 10s", h.Timeout)
		}
		// Command should be resolved to absolute path.
		if !filepath.IsAbs(h.Command) {
			t.Errorf("command should be absolute, got %q", h.Command)
		}
	})

	t.Run("missing fields", func(t *testing.T) {
		dir := t.TempDir()
		hookPath := filepath.Join(dir, "HOOK.md")

		// Missing name.
		os.WriteFile(hookPath, []byte("---\ndescription: test\nevents: [before_llm_call]\ncommand: ./run.sh\n---\nbody"), 0o644)
		if _, err := ParseHook(hookPath); err == nil {
			t.Error("expected error for missing name")
		}

		// Missing description.
		os.WriteFile(hookPath, []byte("---\nname: test\nevents: [before_llm_call]\ncommand: ./run.sh\n---\nbody"), 0o644)
		if _, err := ParseHook(hookPath); err == nil {
			t.Error("expected error for missing description")
		}

		// Missing command.
		os.WriteFile(hookPath, []byte("---\nname: test\ndescription: test\nevents: [before_llm_call]\n---\nbody"), 0o644)
		if _, err := ParseHook(hookPath); err == nil {
			t.Error("expected error for missing command")
		}

		// Missing events.
		os.WriteFile(hookPath, []byte("---\nname: test\ndescription: test\ncommand: ./run.sh\n---\nbody"), 0o644)
		if _, err := ParseHook(hookPath); err == nil {
			t.Error("expected error for missing events")
		}
	})

	t.Run("invalid events filtered", func(t *testing.T) {
		dir := t.TempDir()
		hookPath := filepath.Join(dir, "HOOK.md")
		os.WriteFile(hookPath, []byte("---\nname: test\ndescription: test\nevents: [invalid_event]\ncommand: ./run.sh\n---\nbody"), 0o644)
		if _, err := ParseHook(hookPath); err == nil {
			t.Error("expected error for no valid events")
		}
	})

	t.Run("inline events format", func(t *testing.T) {
		dir := t.TempDir()
		hookPath := filepath.Join(dir, "HOOK.md")
		os.WriteFile(hookPath, []byte("---\nname: test\ndescription: test\nevents: [before_llm_call, after_reset]\ncommand: ./run.sh\n---\nbody"), 0o644)
		h, err := ParseHook(hookPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(h.Events) != 2 {
			t.Errorf("events count = %d, want 2", len(h.Events))
		}
	})

	t.Run("default timeout", func(t *testing.T) {
		dir := t.TempDir()
		hookPath := filepath.Join(dir, "HOOK.md")
		os.WriteFile(hookPath, []byte("---\nname: test\ndescription: test\nevents: [before_llm_call]\ncommand: ./run.sh\n---\nbody"), 0o644)
		h, err := ParseHook(hookPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if h.Timeout != 10*time.Second {
			t.Errorf("timeout = %v, want 10s", h.Timeout)
		}
	})

	t.Run("new event types after_llm_call and user_message", func(t *testing.T) {
		dir := t.TempDir()
		hookPath := filepath.Join(dir, "HOOK.md")
		os.WriteFile(hookPath, []byte("---\nname: analytics\ndescription: Track LLM calls\nevents: [after_llm_call, user_message]\ncommand: ./run.sh\n---\nbody"), 0o644)
		h, err := ParseHook(hookPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(h.Events) != 2 {
			t.Fatalf("events count = %d, want 2", len(h.Events))
		}
		if h.Events[0] != EventAfterLLMCall {
			t.Errorf("events[0] = %q, want %q", h.Events[0], EventAfterLLMCall)
		}
		if h.Events[1] != EventUserMessage {
			t.Errorf("events[1] = %q, want %q", h.Events[1], EventUserMessage)
		}
	})

	t.Run("all four event types", func(t *testing.T) {
		dir := t.TempDir()
		hookPath := filepath.Join(dir, "HOOK.md")
		os.WriteFile(hookPath, []byte("---\nname: all\ndescription: All events\nevents: [before_llm_call, after_llm_call, after_reset, user_message]\ncommand: ./run.sh\n---\nbody"), 0o644)
		h, err := ParseHook(hookPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(h.Events) != 4 {
			t.Fatalf("events count = %d, want 4", len(h.Events))
		}
	})

	t.Run("multiline new event types", func(t *testing.T) {
		dir := t.TempDir()
		hookPath := filepath.Join(dir, "HOOK.md")
		content := "---\nname: multi\ndescription: Multi events\nevents:\n  - after_llm_call\n  - user_message\ncommand: ./run.sh\n---\nbody"
		os.WriteFile(hookPath, []byte(content), 0o644)
		h, err := ParseHook(hookPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(h.Events) != 2 {
			t.Fatalf("events count = %d, want 2", len(h.Events))
		}
		if h.Events[0] != EventAfterLLMCall {
			t.Errorf("events[0] = %q, want %q", h.Events[0], EventAfterLLMCall)
		}
		if h.Events[1] != EventUserMessage {
			t.Errorf("events[1] = %q, want %q", h.Events[1], EventUserMessage)
		}
	})
}

func TestEventTypeIsBlocking(t *testing.T) {
	tests := []struct {
		event EventType
		want  bool
	}{
		{EventBeforeLLMCall, true},
		{EventAfterLLMCall, false},
		{EventAfterReset, false},
		{EventUserMessage, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.event), func(t *testing.T) {
			if got := tt.event.IsBlocking(); got != tt.want {
				t.Errorf("IsBlocking() = %v, want %v", got, tt.want)
			}
		})
	}
}
