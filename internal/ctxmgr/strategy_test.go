package ctxmgr

import (
	"context"
	"strings"
	"testing"

	"github.com/joechenrh/golem/internal/llm"
	"github.com/joechenrh/golem/internal/tape"
)

func TestNewContextStrategy(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"anchor", false},
		{"masking", false},
		{"unknown", true},
	}
	for _, tt := range tests {
		s, err := NewContextStrategy(tt.name)
		if tt.wantErr {
			if err == nil {
				t.Errorf("NewContextStrategy(%q) expected error", tt.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("NewContextStrategy(%q) error: %v", tt.name, err)
			continue
		}
		if s.Name() != tt.name {
			t.Errorf("Name() = %q, want %q", s.Name(), tt.name)
		}
	}
}

func TestAnchorStrategy_BasicMessages(t *testing.T) {
	entries := []tape.TapeEntry{
		{Kind: tape.KindMessage, Payload: map[string]any{"role": "user", "content": "hello"}},
		{Kind: tape.KindMessage, Payload: map[string]any{"role": "assistant", "content": "hi"}},
	}

	s := &AnchorStrategy{}
	msgs, err := s.BuildContext(context.Background(), entries, 128_000)
	if err != nil {
		t.Fatalf("BuildContext() error: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("len(msgs) = %d, want 2", len(msgs))
	}
	if msgs[0].Content != "hello" || msgs[1].Content != "hi" {
		t.Errorf("msgs = %+v", msgs)
	}
}

func TestAnchorStrategy_RespectsAnchors(t *testing.T) {
	entries := []tape.TapeEntry{
		{Kind: tape.KindMessage, Payload: map[string]any{"role": "user", "content": "old"}},
		{Kind: tape.KindAnchor, Payload: map[string]any{"label": "reset"}},
		{Kind: tape.KindMessage, Payload: map[string]any{"role": "user", "content": "new"}},
	}

	s := &AnchorStrategy{}
	msgs, err := s.BuildContext(context.Background(), entries, 128_000)
	if err != nil {
		t.Fatalf("BuildContext() error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len(msgs) = %d, want 1", len(msgs))
	}
	if msgs[0].Content != "new" {
		t.Errorf("content = %q, want %q", msgs[0].Content, "new")
	}
}

func TestAnchorStrategy_TrimToFit(t *testing.T) {
	// Create messages that exceed a tiny maxTokens budget.
	entries := []tape.TapeEntry{
		{Kind: tape.KindMessage, Payload: map[string]any{"role": "user", "content": strings.Repeat("a", 400)}},
		{Kind: tape.KindMessage, Payload: map[string]any{"role": "assistant", "content": strings.Repeat("b", 400)}},
		{Kind: tape.KindMessage, Payload: map[string]any{"role": "user", "content": "last"}},
	}

	s := &AnchorStrategy{}
	// 50 tokens = ~200 chars, should drop oldest messages.
	msgs, err := s.BuildContext(context.Background(), entries, 50)
	if err != nil {
		t.Fatalf("BuildContext() error: %v", err)
	}
	// At minimum the last message should remain.
	if len(msgs) == 0 {
		t.Fatal("expected at least 1 message after trimming")
	}
	if msgs[len(msgs)-1].Content != "last" {
		t.Errorf("last message content = %q, want %q", msgs[len(msgs)-1].Content, "last")
	}
}

func TestMaskingStrategy_NoMaskingUnderThreshold(t *testing.T) {
	longOutput := strings.Repeat("x", 5000)
	entries := []tape.TapeEntry{
		{Kind: tape.KindMessage, Payload: map[string]any{"role": "user", "content": "run tool"}},
		{Kind: tape.KindMessage, Payload: map[string]any{"role": "tool", "content": longOutput, "tool_call_id": "t1"}},
	}

	s := &MaskingStrategy{MaskThreshold: 0.5, MaxOutputChars: 2000}
	// Very large context window — no masking needed.
	msgs, err := s.BuildContext(context.Background(), entries, 1_000_000)
	if err != nil {
		t.Fatalf("BuildContext() error: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("len(msgs) = %d, want 2", len(msgs))
	}
	// Tool output should not be masked.
	if len(msgs[1].Content) != 5000 {
		t.Errorf("tool output length = %d, want 5000 (no masking)", len(msgs[1].Content))
	}
}

func TestMaskingStrategy_MasksWhenOverThreshold(t *testing.T) {
	longOutput := strings.Repeat("x", 5000)
	entries := []tape.TapeEntry{
		{Kind: tape.KindMessage, Payload: map[string]any{"role": "user", "content": "run tool"}},
		{Kind: tape.KindMessage, Payload: map[string]any{"role": "tool", "content": longOutput, "tool_call_id": "t1"}},
	}

	s := &MaskingStrategy{MaskThreshold: 0.5, MaxOutputChars: 2000}
	// Small context window — masking should activate.
	// Total chars ~5008, tokens ~1252, threshold = 0.5 * 1500 = 750.
	msgs, err := s.BuildContext(context.Background(), entries, 1500)
	if err != nil {
		t.Fatalf("BuildContext() error: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("len(msgs) = %d, want 2", len(msgs))
	}
	if !strings.Contains(msgs[1].Content, "[...truncated") {
		t.Error("expected tool output to be truncated")
	}
	if len(msgs[1].Content) >= 5000 {
		t.Errorf("masked output length = %d, should be less than 5000", len(msgs[1].Content))
	}
}

func TestMaskObservations(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: "hello"},
		{Role: llm.RoleTool, Content: strings.Repeat("a", 4000), ToolCallID: "t1"},
		{Role: llm.RoleTool, Content: "short", ToolCallID: "t2"},
	}

	masked := MaskObservations(msgs, 2000)

	// User message should be unchanged.
	if masked[0].Content != "hello" {
		t.Errorf("user msg changed: %q", masked[0].Content)
	}
	// Long tool output should be truncated.
	if !strings.Contains(masked[1].Content, "[...truncated") {
		t.Error("long tool output not truncated")
	}
	if len(masked[1].Content) >= 4000 {
		t.Errorf("masked length = %d, should be < 4000", len(masked[1].Content))
	}
	// Short tool output should be unchanged.
	if masked[2].Content != "short" {
		t.Errorf("short tool output changed: %q", masked[2].Content)
	}

	// Original should not be modified.
	if len(msgs[1].Content) != 4000 {
		t.Error("original message was modified")
	}
}

func TestEstimateTokens(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: strings.Repeat("a", 400)}, // 100 tokens
	}
	got := EstimateTokens(msgs)
	if got != 100 {
		t.Errorf("EstimateTokens (ASCII) = %d, want 100", got)
	}
}

func TestEstimateTokens_CJK(t *testing.T) {
	// 4 CJK characters should be ~4 tokens (1 token each).
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: "你好世界"},
	}
	got := EstimateTokens(msgs)
	if got != 4 {
		t.Errorf("EstimateTokens (CJK) = %d, want 4", got)
	}
}

func TestEstimateTokens_Mixed(t *testing.T) {
	// 8 ASCII chars (2 tokens) + 4 CJK chars (4 tokens) = 6 tokens
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: "hello!! 你好世界测"},
	}
	// "hello!! " = 8 ASCII chars = 2 tokens, "你好世界测" = 5 CJK = 5 tokens
	got := EstimateTokens(msgs)
	want := 8/4 + 5 // 2 + 5 = 7
	if got != want {
		t.Errorf("EstimateTokens (mixed) = %d, want %d", got, want)
	}
}

func TestModelContextWindow(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		{"claude-sonnet-4-20250514", 200_000},
		{"gpt-4o", 128_000},
		{"gpt-4o-mini", 128_000},
		{"some-unknown-model", 128_000},
	}
	for _, tt := range tests {
		got := ModelContextWindow(tt.model)
		if got != tt.want {
			t.Errorf("ModelContextWindow(%q) = %d, want %d", tt.model, got, tt.want)
		}
	}
}
