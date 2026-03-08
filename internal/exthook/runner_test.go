package exthook

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func writeHookScript(t *testing.T, dir, script string) string {
	t.Helper()
	path := filepath.Join(dir, "handler.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunnerBeforeLLMCall(t *testing.T) {
	logger := zap.NewNop()

	t.Run("content injection", func(t *testing.T) {
		dir := t.TempDir()
		cmd := writeHookScript(t, dir, `echo '{"content":"injected context"}'`)

		runner := NewRunner([]*HookDef{{
			Name:    "test-hook",
			Events:  []EventType{EventBeforeLLMCall},
			Command: cmd,
			Dir:     dir,
			Timeout: 5 * time.Second,
		}}, logger)

		result, err := runner.Run(context.Background(), "before_llm_call", "agent", map[string]any{
			"user_message": "hello",
			"iteration":    0,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "injected context" {
			t.Errorf("result = %q, want %q", result, "injected context")
		}
	})

	t.Run("empty stdout", func(t *testing.T) {
		dir := t.TempDir()
		cmd := writeHookScript(t, dir, `echo ''`)

		runner := NewRunner([]*HookDef{{
			Name:    "test-hook",
			Events:  []EventType{EventBeforeLLMCall},
			Command: cmd,
			Dir:     dir,
			Timeout: 5 * time.Second,
		}}, logger)

		result, err := runner.Run(context.Background(), "before_llm_call", "agent", map[string]any{
			"user_message": "hello",
			"iteration":    0,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "" {
			t.Errorf("result = %q, want empty", result)
		}
	})

	t.Run("multiple hooks concatenated", func(t *testing.T) {
		dir1 := t.TempDir()
		cmd1 := writeHookScript(t, dir1, `echo '{"content":"from hook 1"}'`)
		dir2 := t.TempDir()
		cmd2 := writeHookScript(t, dir2, `echo '{"content":"from hook 2"}'`)

		runner := NewRunner([]*HookDef{
			{Name: "h1", Events: []EventType{EventBeforeLLMCall}, Command: cmd1, Dir: dir1, Timeout: 5 * time.Second},
			{Name: "h2", Events: []EventType{EventBeforeLLMCall}, Command: cmd2, Dir: dir2, Timeout: 5 * time.Second},
		}, logger)

		result, err := runner.Run(context.Background(), "before_llm_call", "agent", map[string]any{
			"user_message": "hello",
			"iteration":    0,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, "from hook 1") || !strings.Contains(result, "from hook 2") {
			t.Errorf("result = %q, want both hook outputs", result)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		dir := t.TempDir()
		cmd := writeHookScript(t, dir, `sleep 10; echo '{"content":"late"}'`)

		runner := NewRunner([]*HookDef{{
			Name:    "slow-hook",
			Events:  []EventType{EventBeforeLLMCall},
			Command: cmd,
			Dir:     dir,
			Timeout: 100 * time.Millisecond,
		}}, logger)

		result, err := runner.Run(context.Background(), "before_llm_call", "agent", map[string]any{
			"user_message": "hello",
			"iteration":    0,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Timed out hook should be skipped, not block.
		if result != "" {
			t.Errorf("result = %q, want empty (timed out)", result)
		}
	})

	t.Run("hook not subscribed to event", func(t *testing.T) {
		dir := t.TempDir()
		cmd := writeHookScript(t, dir, `echo '{"content":"should not appear"}'`)

		runner := NewRunner([]*HookDef{{
			Name:    "reset-only",
			Events:  []EventType{EventAfterReset},
			Command: cmd,
			Dir:     dir,
			Timeout: 5 * time.Second,
		}}, logger)

		result, err := runner.Run(context.Background(), "before_llm_call", "agent", map[string]any{
			"user_message": "hello",
			"iteration":    0,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "" {
			t.Errorf("result = %q, want empty (not subscribed)", result)
		}
	})
}

func TestRunnerAfterReset(t *testing.T) {
	logger := zap.NewNop()

	t.Run("receives summary in unified envelope", func(t *testing.T) {
		dir := t.TempDir()
		outFile := filepath.Join(dir, "received.txt")
		// Write stdin to a file so we can verify it.
		cmd := writeHookScript(t, dir, `cat > `+outFile)

		runner := NewRunner([]*HookDef{{
			Name:    "test-hook",
			Events:  []EventType{EventAfterReset},
			Command: cmd,
			Dir:     dir,
			Timeout: 5 * time.Second,
		}}, logger)

		runner.Run(context.Background(), "after_reset", "atlas", map[string]any{
			"summary": "test summary",
		})

		data, err := os.ReadFile(outFile)
		if err != nil {
			t.Fatalf("reading output: %v", err)
		}
		got := string(data)
		if !strings.Contains(got, `"test summary"`) {
			t.Errorf("hook did not receive summary: %s", got)
		}
		if !strings.Contains(got, `"after_reset"`) {
			t.Errorf("hook did not receive event type: %s", got)
		}
		if !strings.Contains(got, `"atlas"`) {
			t.Errorf("hook did not receive agent name: %s", got)
		}
		// Verify unified envelope: data should be nested under "data" key.
		if !strings.Contains(got, `"data":{`) || !strings.Contains(got, `"data": {`) {
			// Check for either compact or pretty JSON.
			if !strings.Contains(got, `"data"`) {
				t.Errorf("hook did not receive unified envelope with data key: %s", got)
			}
		}
	})
}

func TestRunnerAfterLLMCall(t *testing.T) {
	logger := zap.NewNop()

	t.Run("fire and forget returns empty", func(t *testing.T) {
		dir := t.TempDir()
		cmd := writeHookScript(t, dir, `echo '{"content":"should be ignored"}'`)

		runner := NewRunner([]*HookDef{{
			Name:    "analytics",
			Events:  []EventType{EventAfterLLMCall},
			Command: cmd,
			Dir:     dir,
			Timeout: 5 * time.Second,
		}}, logger)

		result, err := runner.Run(context.Background(), "after_llm_call", "agent", map[string]any{
			"finish_reason":     "stop",
			"tool_call_count":   0,
			"prompt_tokens":     100,
			"completion_tokens": 50,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "" {
			t.Errorf("result = %q, want empty (non-blocking event)", result)
		}
	})
}

func TestRunnerUserMessage(t *testing.T) {
	logger := zap.NewNop()

	t.Run("fire and forget returns empty", func(t *testing.T) {
		dir := t.TempDir()
		outFile := filepath.Join(dir, "received.txt")
		cmd := writeHookScript(t, dir, `cat > `+outFile)

		runner := NewRunner([]*HookDef{{
			Name:    "audit",
			Events:  []EventType{EventUserMessage},
			Command: cmd,
			Dir:     dir,
			Timeout: 5 * time.Second,
		}}, logger)

		result, err := runner.Run(context.Background(), "user_message", "agent", map[string]any{
			"text":       "hello world",
			"channel_id": "lark:oc_123",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "" {
			t.Errorf("result = %q, want empty (non-blocking event)", result)
		}

		// Verify the hook received the payload.
		data, err := os.ReadFile(outFile)
		if err != nil {
			t.Fatalf("reading output: %v", err)
		}
		got := string(data)
		if !strings.Contains(got, "hello world") {
			t.Errorf("hook did not receive text: %s", got)
		}
		if !strings.Contains(got, "user_message") {
			t.Errorf("hook did not receive event type: %s", got)
		}
	})
}
