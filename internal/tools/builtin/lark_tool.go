package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	larkchan "github.com/joechenrh/golem/internal/channel/lark"
	"github.com/joechenrh/golem/internal/llm"
)

// LarkSendTool lets the agent send messages and images to Lark group chats.
type LarkSendTool struct {
	ch         *larkchan.LarkChannel
	httpClient *http.Client
}

func NewLarkSendTool(
	ch *larkchan.LarkChannel,
	httpClient *http.Client,
) *LarkSendTool {
	return &LarkSendTool{ch: ch, httpClient: httpClient}
}

func (t *LarkSendTool) Name() string        { return "lark_send" }
func (t *LarkSendTool) Description() string { return "Send a message or image to a Lark group chat" }
func (t *LarkSendTool) FullDescription() string {
	return "Send a text message and/or image to a Lark/Feishu group chat. " +
		"At least one of 'message' or 'image' must be provided. " +
		"Use lark_list_chats first to find the chat_id of the target group."
}

var larkSendParams = json.RawMessage(`{
	"type": "object",
	"properties": {
		"chat_id": {"type": "string", "description": "The chat_id of the target Lark group"},
		"message": {"type": "string", "description": "The text message to send (optional if image is provided)"},
		"image": {"type": "string", "description": "URL of an image to download and send (optional if message is provided)"}
	},
	"required": ["chat_id"]
}`)

func (t *LarkSendTool) Parameters() json.RawMessage { return larkSendParams }

func (t *LarkSendTool) Execute(
	ctx context.Context, args string,
) (string, error) {
	var params struct {
		ChatID  string `json:"chat_id"`
		Message string `json:"message"`
		Image   string `json:"image"`
	}
	if err := json.Unmarshal([]byte(llm.NormalizeArgs(args)), &params); err != nil {
		return "Error: invalid arguments: " + err.Error(), nil
	}
	if params.ChatID == "" {
		return "Error: 'chat_id' is required", nil
	}
	if params.Message == "" && params.Image == "" {
		return "Error: at least one of 'message' or 'image' must be provided", nil
	}

	var results []string

	// Send text card if message is provided.
	if params.Message != "" {
		if err := t.ch.SendToChat(ctx, params.ChatID, params.Message); err != nil {
			return "Error sending message: " + err.Error(), nil
		}
		results = append(results, "text message sent")
	}

	// Download and send image if URL is provided.
	if params.Image != "" {
		imgData, err := t.downloadURL(ctx, params.Image)
		if err != nil {
			return "Error downloading image: " + err.Error(), nil
		}

		imageKey, err := t.ch.UploadImage(ctx, bytes.NewReader(imgData))
		if err != nil {
			return "Error uploading image to Lark: " + err.Error(), nil
		}

		if err := t.ch.SendImageToChat(ctx, params.ChatID, imageKey); err != nil {
			return "Error sending image: " + err.Error(), nil
		}
		results = append(results, "image sent")
	}

	return fmt.Sprintf("Success: %s to chat %s.", strings.Join(results, " and "), params.ChatID), nil
}

// downloadURL fetches the content at the given URL.
func (t *LarkSendTool) downloadURL(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// LarkListChatsTool lets the agent discover which Lark groups the bot belongs to.
type LarkListChatsTool struct {
	ch *larkchan.LarkChannel
}

func NewLarkListChatsTool(
	ch *larkchan.LarkChannel,
) *LarkListChatsTool {
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

func (t *LarkListChatsTool) Execute(
	ctx context.Context, args string,
) (string, error) {
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
