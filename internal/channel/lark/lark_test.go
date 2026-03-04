package lark

import (
	"testing"

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
