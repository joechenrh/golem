package lark

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

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

func TestSanitizeLarkMarkdown(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			"h1 to bold",
			"# Title",
			"**Title**",
		},
		{
			"h2 to bold",
			"## Section",
			"**Section**",
		},
		{
			"h3 to bold",
			"### Subsection",
			"**Subsection**",
		},
		{
			"blockquote to italic",
			"> some quote",
			"*some quote*",
		},
		{
			"blockquote without space",
			">no space",
			"*no space*",
		},
		{
			"mixed content",
			"# Title\nsome text\n## Sub\n> note\nmore text",
			"**Title**\nsome text\n**Sub**\n*note*\nmore text",
		},
		{
			"no change for supported syntax",
			"**bold** and *italic* and `code`",
			"**bold** and *italic* and code",
		},
		{
			"code block not touched",
			"```\n# not a header\n> not a quote\n```",
			"```\n# not a header\n> not a quote\n```",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeLarkMarkdown(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeLarkMarkdown(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractPostContent(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantText   string
		wantImages []string
	}{
		{
			"text only",
			`{"zh_cn":{"title":"","content":[[{"tag":"text","text":"hello world"}]]}}`,
			"hello world",
			nil,
		},
		{
			"with title",
			`{"zh_cn":{"title":"My Title","content":[[{"tag":"text","text":"body"}]]}}`,
			"My Title\nbody",
			nil,
		},
		{
			"image only",
			`{"zh_cn":{"title":"","content":[[{"tag":"img","image_key":"img_abc123"}]]}}`,
			"",
			[]string{"img_abc123"},
		},
		{
			"text and image",
			`{"zh_cn":{"title":"","content":[[{"tag":"text","text":"check this "},{"tag":"img","image_key":"img_xyz"}]]}}`,
			"check this",
			[]string{"img_xyz"},
		},
		{
			"multiple images",
			`{"zh_cn":{"title":"","content":[[{"tag":"img","image_key":"img_1"}],[{"tag":"img","image_key":"img_2"}]]}}`,
			"",
			[]string{"img_1", "img_2"},
		},
		{
			"en_us locale",
			`{"en_us":{"title":"","content":[[{"tag":"text","text":"english"}]]}}`,
			"english",
			nil,
		},
		{
			"invalid json",
			`not json`,
			"",
			nil,
		},
		{
			"empty content",
			`{"zh_cn":{"title":"","content":[]}}`,
			"",
			nil,
		},
		{
			"direct format text",
			`{"title":"","content":[[{"tag":"text","text":"direct msg"}]]}`,
			"direct msg",
			nil,
		},
		{
			"direct format image",
			`{"title":"","content":[[{"tag":"img","image_key":"img_direct"}]]}`,
			"",
			[]string{"img_direct"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotText, gotImages := extractPostContent(tt.input)
			if gotText != tt.wantText {
				t.Errorf("text = %q, want %q", gotText, tt.wantText)
			}
			if len(gotImages) != len(tt.wantImages) {
				t.Fatalf("images len = %d, want %d", len(gotImages), len(tt.wantImages))
			}
			for i, key := range gotImages {
				if key != tt.wantImages[i] {
					t.Errorf("images[%d] = %q, want %q", i, key, tt.wantImages[i])
				}
			}
		})
	}
}

func TestSendSkipsDuplicateChat(t *testing.T) {
	lc := &LarkChannel{logger: zap.NewNop()}
	lc.sentChats.Store("chat_123", true)

	// Send to a chat that was already sent to — should be a no-op (no client, so
	// calling sendCard would panic).
	err := lc.Send(context.Background(), channel.OutgoingMessage{
		ChannelID: "chat_123",
		Text:      "duplicate",
	})
	if err != nil {
		t.Fatalf("Send to duplicate chat returned error: %v", err)
	}
}

func TestSeenMsgsEviction_OldEntriesRemoved(t *testing.T) {
	lc := &LarkChannel{logger: zap.NewNop()}

	// Insert 15000 entries with old timestamps (well beyond any maxAge).
	oldTime := time.Now().Add(-1 * time.Hour)
	for i := range 15000 {
		lc.seenMsgs.Store(fmt.Sprintf("msg-%d", i), oldTime)
	}

	// Run the eviction loop with a very short maxAge so entries are immediately
	// considered expired. Cancel the context after the first tick fires.
	maxAge := 10 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	lc.seenMsgsEvictionLoop(ctx, maxAge)

	// Count remaining entries -- all should be evicted because they are older
	// than maxAge.
	remaining := 0
	lc.seenMsgs.Range(func(_, _ any) bool {
		remaining++
		return true
	})
	if remaining > 0 {
		t.Errorf("expected 0 remaining entries after eviction of old entries, got %d", remaining)
	}
}

func TestSeenMsgsEviction_CapEnforced(t *testing.T) {
	lc := &LarkChannel{logger: zap.NewNop()}

	// Insert 15000 entries with recent timestamps so age-based eviction
	// won't remove them. This tests the force-evict cap logic.
	now := time.Now()
	for i := range 15000 {
		lc.seenMsgs.Store(fmt.Sprintf("msg-%d", i), now)
	}

	// Use a long maxAge so none expire by age, but the cap (maxSeenMsgs=10000)
	// triggers force-eviction.
	maxAge := 10 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	lc.seenMsgsEvictionLoop(ctx, maxAge)

	remaining := 0
	lc.seenMsgs.Range(func(_, _ any) bool {
		remaining++
		return true
	})
	if remaining > maxSeenMsgs {
		t.Errorf("expected at most %d remaining entries after cap enforcement, got %d", maxSeenMsgs, remaining)
	}
}

func TestSendToChatRecordsChatID(t *testing.T) {
	lc := &LarkChannel{logger: zap.NewNop()}
	lc.sentChats.Store("chat_456", true)

	// Send should skip because chat_456 was already "sent to".
	err := lc.Send(context.Background(), channel.OutgoingMessage{
		ChannelID: "chat_456",
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
		ChannelID: "chat_789",
		Text:      "should attempt send",
	})
}
