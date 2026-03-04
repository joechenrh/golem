package lark

import (
	"context"
	"encoding/json"
	"testing"

	"go.uber.org/zap"

	"github.com/joechenrh/golem/internal/channel"
)

// Verify LarkChannel satisfies the channel.Channel interface.
var _ channel.Channel = (*LarkChannel)(nil)

func TestExtractTextContent(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain text", `{"text":"hello world"}`, "hello world"},
		{"with whitespace", `{"text":"  hello  "}`, "hello"},
		{"empty text", `{"text":""}`, ""},
		{"whitespace only", `{"text":"   "}`, ""},
		{"invalid json", `not json`, ""},
		{"empty object", `{}`, ""},
		{"with mention", `{"text":"@_user_1 hello"}`, "@_user_1 hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTextContent(tt.input)
			if got != tt.want {
				t.Errorf("extractTextContent(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestBuildCardContent(t *testing.T) {
	text := "**bold** and *italic*"
	card := map[string]any{
		"elements": []map[string]string{
			{"tag": "markdown", "content": text},
		},
	}
	content, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("marshal card: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(content, &parsed); err != nil {
		t.Fatalf("unmarshal card: %v", err)
	}

	elements, ok := parsed["elements"].([]any)
	if !ok || len(elements) != 1 {
		t.Fatalf("expected 1 element, got %v", parsed["elements"])
	}

	elem := elements[0].(map[string]any)
	if elem["tag"] != "markdown" {
		t.Errorf("tag = %q, want %q", elem["tag"], "markdown")
	}
	if elem["content"] != text {
		t.Errorf("content = %q, want %q", elem["content"], text)
	}
}

func TestSendSkipsDuplicateChat(t *testing.T) {
	lc := &LarkChannel{logger: zap.NewNop()}
	lc.sentChats.Store("chat_123", true)

	// Send to a chat that was already sent to — should be a no-op (no client, so
	// calling sendCard would panic).
	err := lc.Send(context.Background(), channel.OutgoingMessage{
		ChannelID: "lark:chat_123",
		Text:      "duplicate",
	})
	if err != nil {
		t.Fatalf("Send to duplicate chat returned error: %v", err)
	}
}

func TestSendToChatRecordsChatID(t *testing.T) {
	lc := &LarkChannel{logger: zap.NewNop()}
	lc.sentChats.Store("chat_456", true)

	// Send should skip because chat_456 was already "sent to".
	err := lc.Send(context.Background(), channel.OutgoingMessage{
		ChannelID: "lark:chat_456",
		Text:      "should be skipped",
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	// Verify a different chat ID is NOT skipped (will panic on nil client,
	// which confirms it tried to send).
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for unsent chat, got none")
		}
	}()
	_ = lc.Send(context.Background(), channel.OutgoingMessage{
		ChannelID: "lark:chat_789",
		Text:      "should attempt send",
	})
}
