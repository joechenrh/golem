package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/joechenrh/golem/internal/tools"
)

// DirectSender sends a message to a specific channel. Satisfied by
// *larkchan.LarkChannel and test mocks.
type DirectSender interface {
	SendDirect(ctx context.Context, channelID, text string) error
}

// LarkMessageTool sends a plain text message to a Lark chat.
// Designed for sub-agents that need to notify users but don't have
// direct channel access.
type LarkMessageTool struct {
	sender DirectSender
}

func NewLarkMessageTool(sender DirectSender) *LarkMessageTool {
	return &LarkMessageTool{sender: sender}
}

func (t *LarkMessageTool) Name() string        { return "lark_message" }
func (t *LarkMessageTool) Description() string { return "Send a text message to a Lark chat" }
func (t *LarkMessageTool) FullDescription() string {
	return "Send a text message to a Lark/Feishu chat by channel ID. " +
		"Use this to notify users about task progress or completion. " +
		"The channel_id is provided in your task context — do not call lark_list_chats."
}

var larkMessageParams = json.RawMessage(`{
	"type": "object",
	"properties": {
		"channel_id": {"type": "string", "description": "The channel ID of the target Lark chat"},
		"text": {"type": "string", "description": "The message text to send"}
	},
	"required": ["channel_id", "text"]
}`)

func (t *LarkMessageTool) Parameters() json.RawMessage { return larkMessageParams }

func (t *LarkMessageTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		ChannelID string `json:"channel_id"`
		Text      string `json:"text"`
	}
	if errMsg := tools.ParseArgs(args, &params); errMsg != "" {
		return errMsg, nil
	}
	if params.ChannelID == "" {
		return "Error: 'channel_id' is required", nil
	}
	if params.Text == "" {
		return "Error: 'text' is required", nil
	}

	if err := t.sender.SendDirect(ctx, params.ChannelID, params.Text); err != nil {
		return "Error sending message: " + err.Error(), nil
	}
	return fmt.Sprintf("Message sent to %s", params.ChannelID), nil
}
