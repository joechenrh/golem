package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	larkchan "github.com/joechenrh/golem/internal/channel/lark"
	"github.com/joechenrh/golem/internal/llm"
)

// LarkSendTool lets the agent send messages to Lark group chats.
type LarkSendTool struct {
	ch *larkchan.LarkChannel
}

func NewLarkSendTool(ch *larkchan.LarkChannel) *LarkSendTool {
	return &LarkSendTool{ch: ch}
}

func (t *LarkSendTool) Name() string        { return "lark_send" }
func (t *LarkSendTool) Description() string { return "Send a message to a Lark group chat" }
func (t *LarkSendTool) FullDescription() string {
	return "Send a text message to a Lark/Feishu group chat. " +
		"Use lark_list_chats first to find the chat_id of the target group."
}

var larkSendParams = json.RawMessage(`{
	"type": "object",
	"properties": {
		"chat_id": {"type": "string", "description": "The chat_id of the target Lark group"},
		"message": {"type": "string", "description": "The text message to send"}
	},
	"required": ["chat_id", "message"]
}`)

func (t *LarkSendTool) Parameters() json.RawMessage { return larkSendParams }

func (t *LarkSendTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		ChatID  string `json:"chat_id"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(llm.NormalizeArgs(args)), &params); err != nil {
		return "Error: invalid arguments: " + err.Error(), nil
	}
	if params.ChatID == "" {
		return "Error: 'chat_id' is required", nil
	}
	if params.Message == "" {
		return "Error: 'message' is required", nil
	}

	if err := t.ch.SendToChat(ctx, params.ChatID, params.Message); err != nil {
		return "Error: " + err.Error(), nil
	}
	return fmt.Sprintf("Message sent to chat %s successfully.", params.ChatID), nil
}

// LarkListChatsTool lets the agent discover which Lark groups the bot belongs to.
type LarkListChatsTool struct {
	ch *larkchan.LarkChannel
}

func NewLarkListChatsTool(ch *larkchan.LarkChannel) *LarkListChatsTool {
	return &LarkListChatsTool{ch: ch}
}

func (t *LarkListChatsTool) Name() string        { return "lark_list_chats" }
func (t *LarkListChatsTool) Description() string { return "List Lark group chats the bot belongs to" }
func (t *LarkListChatsTool) FullDescription() string {
	return "List all Lark/Feishu group chats that the bot is a member of. " +
		"Returns chat_id, name, and description for each group. " +
		"Use this to find the chat_id before sending a message with lark_send."
}

var larkListChatsParams = json.RawMessage(`{
	"type": "object",
	"properties": {}
}`)

func (t *LarkListChatsTool) Parameters() json.RawMessage { return larkListChatsParams }

func (t *LarkListChatsTool) Execute(ctx context.Context, args string) (string, error) {
	chats, err := t.ch.ListChats(ctx)
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	if len(chats) == 0 {
		return "No group chats found. Make sure the bot has been added to at least one group.", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d group(s):\n\n", len(chats)))
	for _, c := range chats {
		sb.WriteString(fmt.Sprintf("- Name: %s\n  chat_id: %s\n", c.Name, c.ChatID))
		if c.Description != "" {
			sb.WriteString(fmt.Sprintf("  Description: %s\n", c.Description))
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}
