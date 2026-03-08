package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/joechenrh/golem/internal/channel"
	larkchan "github.com/joechenrh/golem/internal/channel/lark"
	"github.com/joechenrh/golem/internal/tools"
)

// ChatHistoryTool lets the agent fetch recent messages from a Lark chat.
type ChatHistoryTool struct {
	ch *larkchan.LarkChannel
}

func NewChatHistoryTool(ch *larkchan.LarkChannel) *ChatHistoryTool {
	return &ChatHistoryTool{ch: ch}
}

func (t *ChatHistoryTool) Name() string { return "chat_history" }
func (t *ChatHistoryTool) Description() string {
	return "Fetch recent messages from a Lark chat to review conversation history"
}
func (t *ChatHistoryTool) FullDescription() string {
	return "Fetch recent messages from a Lark/Feishu chat. " +
		"Use this when asked about previous conversations or chat history. " +
		"Returns messages in chronological order with sender info and timestamps."
}

var chatHistoryParams = json.RawMessage(`{
	"type": "object",
	"properties": {
		"chat_id": {"type": "string", "description": "The chat_id to fetch history from. If omitted, uses the current chat."},
		"count": {"type": "integer", "description": "Number of recent messages to fetch (default: 20, max: 50)"}
	}
}`)

func (t *ChatHistoryTool) Parameters() json.RawMessage { return chatHistoryParams }

func (t *ChatHistoryTool) Execute(
	ctx context.Context, args string,
) (string, error) {
	var params struct {
		ChatID string `json:"chat_id"`
		Count  int    `json:"count"`
	}
	if errMsg := tools.ParseArgs(args, &params); errMsg != "" {
		return errMsg, nil
	}

	// Try to get chat_id from context if not provided.
	if params.ChatID == "" {
		params.ChatID = channel.ChannelIDFromContext(ctx)
	}
	if params.ChatID == "" {
		return "Error: 'chat_id' is required — provide it explicitly or use this tool from a Lark chat", nil
	}

	if params.Count <= 0 {
		params.Count = 20
	}

	msgs, err := t.ch.ListMessages(ctx, params.ChatID, params.Count)
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	if len(msgs) == 0 {
		return "No messages found in this chat.", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Recent %d message(s):\n\n", len(msgs)))
	for _, m := range msgs {
		ts := formatTimestamp(m.CreateTime)
		sender := m.SenderID
		if m.SenderType == "app" {
			sender += " (bot)"
		}
		sb.WriteString(fmt.Sprintf("[%s] %s: %s\n", ts, sender, m.Content))
	}
	return sb.String(), nil
}

// formatTimestamp converts a Lark millisecond timestamp string to a readable format.
func formatTimestamp(ms string) string {
	msInt, err := strconv.ParseInt(ms, 10, 64)
	if err != nil {
		return ms
	}
	t := time.UnixMilli(msInt)
	return t.Format("01-02 15:04")
}
