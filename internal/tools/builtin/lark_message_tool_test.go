package builtin

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// mockDirectSender records SendDirect calls for testing.
type mockDirectSender struct {
	calls []struct {
		channelID string
		text      string
	}
	err error
}

func (m *mockDirectSender) SendDirect(_ context.Context, channelID, text string) error {
	m.calls = append(m.calls, struct {
		channelID string
		text      string
	}{channelID, text})
	return m.err
}

func TestLarkMessageTool_Execute(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		wantMsg string
		wantErr bool
	}{
		{
			name:    "success",
			args:    `{"channel_id": "oc_123", "text": "hello"}`,
			wantMsg: "Message sent to oc_123",
		},
		{
			name:    "missing channel_id",
			args:    `{"text": "hello"}`,
			wantMsg: "Error: 'channel_id' is required",
		},
		{
			name:    "missing text",
			args:    `{"channel_id": "oc_123"}`,
			wantMsg: "Error: 'text' is required",
		},
		{
			name:    "empty args",
			args:    `{}`,
			wantMsg: "Error: 'channel_id' is required",
		},
		{
			name:    "malformed JSON",
			args:    `not json`,
			wantMsg: "Error: invalid arguments",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sender := &mockDirectSender{}
			tool := NewLarkMessageTool(sender)

			result, err := tool.Execute(context.Background(), tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Execute() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.name == "malformed JSON" {
				if !strings.HasPrefix(result, tt.wantMsg) {
					t.Errorf("Execute() = %q, want prefix %q", result, tt.wantMsg)
				}
			} else if result != tt.wantMsg {
				t.Errorf("Execute() = %q, want %q", result, tt.wantMsg)
			}
		})
	}
}

func TestLarkMessageTool_Execute_SendError(t *testing.T) {
	sender := &mockDirectSender{err: errors.New("connection refused")}
	tool := NewLarkMessageTool(sender)

	result, err := tool.Execute(context.Background(), `{"channel_id": "oc_123", "text": "hello"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	want := "Error sending message: connection refused"
	if result != want {
		t.Errorf("Execute() = %q, want %q", result, want)
	}
}

func TestLarkMessageTool_Execute_SendsCorrectMessage(t *testing.T) {
	sender := &mockDirectSender{}
	tool := NewLarkMessageTool(sender)

	tool.Execute(context.Background(), `{"channel_id": "oc_abc", "text": "test msg"}`)

	if len(sender.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(sender.calls))
	}
	if sender.calls[0].channelID != "oc_abc" {
		t.Errorf("channelID = %q, want %q", sender.calls[0].channelID, "oc_abc")
	}
	if sender.calls[0].text != "test msg" {
		t.Errorf("text = %q, want %q", sender.calls[0].text, "test msg")
	}
}
