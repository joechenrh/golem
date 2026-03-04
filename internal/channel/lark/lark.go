package lark

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"go.uber.org/zap"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/joechenrh/golem/internal/channel"
)

// LarkChannel implements channel.Channel for Lark/Feishu bot integration
// using long-lived WebSocket connections (no public URL required).
type LarkChannel struct {
	appID      string
	appSecret  string
	client     *lark.Client
	dispatcher *dispatcher.EventDispatcher
	logger     *zap.Logger

	// sentChats tracks chat IDs that have already been sent to during the
	// current message-processing cycle, preventing duplicate replies when
	// both a tool call and processMessage send to the same chat.
	sentMu    sync.Mutex
	sentChats map[string]bool
}

// New creates a LarkChannel with the given credentials.
func New(appID, appSecret, verifyToken string, logger *zap.Logger) *LarkChannel {
	sdkLogger := &zapLarkLogger{z: logger.Named("lark-sdk")}

	client := lark.NewClient(appID, appSecret,
		lark.WithEnableTokenCache(true),
		lark.WithLogLevel(larkcore.LogLevelInfo),
		lark.WithLogger(sdkLogger),
	)

	eventDispatcher := dispatcher.NewEventDispatcher(verifyToken, "")

	return &LarkChannel{
		appID:      appID,
		appSecret:  appSecret,
		client:     client,
		dispatcher: eventDispatcher,
		logger:     logger,
	}
}

func (l *LarkChannel) Name() string { return "lark" }

// Start connects to Lark via WebSocket and dispatches incoming messages to inCh.
// Blocks until the context is cancelled or the connection is permanently lost.
func (l *LarkChannel) Start(ctx context.Context, inCh chan<- channel.IncomingMessage) error {
	sdkLogger := &zapLarkLogger{z: l.logger.Named("lark-ws")}

	l.dispatcher.OnP2MessageReceiveV1(func(_ context.Context, event *larkim.P2MessageReceiveV1) error {
		l.onMessageReceive(event, inCh)
		return nil
	})

	wsClient := larkws.NewClient(l.appID, l.appSecret,
		larkws.WithEventHandler(l.dispatcher),
		larkws.WithLogLevel(larkcore.LogLevelInfo),
		larkws.WithLogger(sdkLogger),
	)

	return wsClient.Start(ctx)
}

func (l *LarkChannel) onMessageReceive(event *larkim.P2MessageReceiveV1, inCh chan<- channel.IncomingMessage) {
	msg := event.Event.Message
	if msg == nil || msg.MessageType == nil || *msg.MessageType != "text" {
		return
	}
	if msg.Content == nil || msg.ChatId == nil {
		return
	}

	text := extractTextContent(*msg.Content)
	if text == "" {
		return
	}

	// Strip @bot mentions.
	if mentions := msg.Mentions; len(mentions) > 0 {
		for _, m := range mentions {
			if m.Key != nil {
				text = strings.ReplaceAll(text, *m.Key, "")
			}
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
	}

	var senderID string
	if event.Event.Sender != nil && event.Event.Sender.SenderId != nil {
		if event.Event.Sender.SenderId.OpenId != nil {
			senderID = *event.Event.Sender.SenderId.OpenId
		}
	}

	// Reset per-cycle duplicate tracking before dispatching.
	l.sentMu.Lock()
	l.sentChats = make(map[string]bool)
	l.sentMu.Unlock()

	done := make(chan struct{})
	inCh <- channel.IncomingMessage{
		ChannelID:   "lark:" + *msg.ChatId,
		ChannelName: "lark",
		SenderID:    senderID,
		Text:        text,
		Done:        done,
	}
	<-done
}

// Send sends a message to the chat identified in msg.ChannelID.
// If the chat was already sent to during this cycle (e.g. by a tool call),
// the send is skipped to avoid duplicate replies.
func (l *LarkChannel) Send(ctx context.Context, msg channel.OutgoingMessage) error {
	chatID := strings.TrimPrefix(msg.ChannelID, "lark:")

	l.sentMu.Lock()
	alreadySent := l.sentChats[chatID]
	l.sentMu.Unlock()

	if alreadySent {
		l.logger.Debug("skipping duplicate send", zap.String("chat_id", chatID))
		return nil
	}
	return l.sendCard(ctx, chatID, msg.Text)
}

// SendTyping is a no-op for Lark.
func (l *LarkChannel) SendTyping(_ context.Context, _ string) error { return nil }

// SupportsStreaming returns false; Lark uses non-streaming responses.
func (l *LarkChannel) SupportsStreaming() bool { return false }

// SendStream collects all tokens and sends as a single message.
func (l *LarkChannel) SendStream(ctx context.Context, channelID string, tokenCh <-chan string) error {
	var sb strings.Builder
	for tok := range tokenCh {
		sb.WriteString(tok)
	}
	return l.Send(ctx, channel.OutgoingMessage{ChannelID: channelID, Text: sb.String()})
}

// SendToChat sends a message to a specific chat_id. Exported for use by tools.
// It records the chat_id so that a subsequent Send to the same chat is skipped.
func (l *LarkChannel) SendToChat(ctx context.Context, chatID, text string) error {
	l.sentMu.Lock()
	if l.sentChats == nil {
		l.sentChats = make(map[string]bool)
	}
	l.sentChats[chatID] = true
	l.sentMu.Unlock()

	return l.sendCard(ctx, chatID, text)
}

// sendCard sends a message as an interactive card with a markdown element,
// which supports bold, italic, strikethrough, links, and code formatting.
func (l *LarkChannel) sendCard(ctx context.Context, chatID, text string) error {
	card := map[string]interface{}{
		"elements": []map[string]string{
			{"tag": "markdown", "content": text},
		},
	}
	content, _ := json.Marshal(card)

	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(
			larkim.NewCreateMessageReqBodyBuilder().
				ReceiveId(chatID).
				MsgType("interactive").
				Content(string(content)).
				Build(),
		).
		Build()

	resp, err := l.client.Im.V1.Message.Create(ctx, req)
	if err != nil {
		return fmt.Errorf("lark send: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("lark send: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// ListChats returns the groups the bot is a member of (for discovering chat_ids).
func (l *LarkChannel) ListChats(ctx context.Context) ([]ChatInfo, error) {
	req := larkim.NewListChatReqBuilder().
		PageSize(50).
		Build()

	resp, err := l.client.Im.V1.Chat.List(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("lark list chats: %w", err)
	}
	if !resp.Success() {
		return nil, fmt.Errorf("lark list chats: code=%d msg=%s", resp.Code, resp.Msg)
	}

	var chats []ChatInfo
	for _, item := range resp.Data.Items {
		ci := ChatInfo{}
		if item.ChatId != nil {
			ci.ChatID = *item.ChatId
		}
		if item.Name != nil {
			ci.Name = *item.Name
		}
		if item.Description != nil {
			ci.Description = *item.Description
		}
		chats = append(chats, ci)
	}
	return chats, nil
}

// ChatInfo holds basic group chat metadata.
type ChatInfo struct {
	ChatID      string
	Name        string
	Description string
}

// extractTextContent parses the Lark message content JSON and returns the text value.
// Lark text messages have the format: {"text":"actual message"}.
func extractTextContent(content string) string {
	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Text)
}

// zapLarkLogger adapts zap.Logger to the Lark SDK's Logger interface.
type zapLarkLogger struct {
	z *zap.Logger
}

func (l *zapLarkLogger) Debug(_ context.Context, args ...interface{}) {
	l.z.Debug(fmt.Sprint(args...))
}

func (l *zapLarkLogger) Info(_ context.Context, args ...interface{}) {
	l.z.Info(fmt.Sprint(args...))
}

func (l *zapLarkLogger) Warn(_ context.Context, args ...interface{}) {
	l.z.Warn(fmt.Sprint(args...))
}

func (l *zapLarkLogger) Error(_ context.Context, args ...interface{}) {
	l.z.Error(fmt.Sprint(args...))
}
