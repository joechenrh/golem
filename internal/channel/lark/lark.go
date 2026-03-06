package lark

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkdocx "github.com/larksuite/oapi-sdk-go/v3/service/docx/v1"
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
	sentChats sync.Map

	// seenMsgs deduplicates incoming Lark events. The Lark WebSocket SDK
	// uses at-least-once delivery and may redeliver the same event if the
	// handler takes too long (e.g. waiting for an LLM response). We track
	// recently seen message IDs to discard duplicates.
	seenMsgs sync.Map

	// startedAt is the process startup time. Messages with a Lark
	// CreateTime before this are stale redeliveries from a previous
	// process and are dropped.
	startedAt time.Time
}

// New creates a LarkChannel with the given credentials.
func New(
	appID, appSecret, verifyToken string,
	logger *zap.Logger,
) *LarkChannel {
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
		startedAt:  time.Now(),
	}
}

func (l *LarkChannel) Name() string { return "lark" }

// Start connects to Lark via WebSocket and dispatches incoming messages to inCh.
// Blocks until the context is cancelled or the connection is permanently lost.
func (l *LarkChannel) Start(
	ctx context.Context,
	inCh chan<- channel.IncomingMessage,
) error {
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

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		l.seenMsgsEvictionLoop(gctx, 5*time.Minute)
		return nil
	})
	g.Go(func() error {
		return wsClient.Start(gctx)
	})
	return g.Wait()
}

func (l *LarkChannel) onMessageReceive(
	event *larkim.P2MessageReceiveV1,
	inCh chan<- channel.IncomingMessage,
) {
	msg := event.Event.Message
	if msg == nil || msg.MessageType == nil || *msg.MessageType != "text" {
		return
	}
	if msg.Content == nil || msg.ChatId == nil {
		return
	}

	// Deduplicate: Lark WebSocket uses at-least-once delivery and may
	// redeliver the same event while the handler is blocked on a slow
	// LLM call.  Use the message ID to detect and discard duplicates.
	var msgID string
	if msg.MessageId != nil {
		msgID = *msg.MessageId
	}
	if msgID != "" {
		if _, dup := l.seenMsgs.LoadOrStore(msgID, time.Now()); dup {
			l.logger.Info("dropping duplicate lark event",
				zap.String("message_id", msgID),
				zap.String("chat_id", *msg.ChatId))
			return
		}
	}

	// Drop stale redeliveries from before this process started.
	if msg.CreateTime != nil {
		if createMs, err := strconv.ParseInt(*msg.CreateTime, 10, 64); err == nil {
			createAt := time.UnixMilli(createMs)
			if createAt.Before(l.startedAt) {
				l.logger.Info("dropping stale lark message from before startup",
					zap.String("message_id", msgID),
					zap.Time("create_time", createAt))
				return
			}
		}
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

	l.logger.Info("received lark message",
		zap.String("message_id", msgID),
		zap.String("chat_id", *msg.ChatId),
		zap.String("sender", senderID),
		zap.String("text", truncateForLog(text, 80)))

	// Reset per-cycle duplicate tracking before dispatching.
	l.sentChats.Clear()

	done := make(chan struct{})
	inCh <- channel.IncomingMessage{
		ChannelID:   *msg.ChatId,
		ChannelName: "lark",
		SenderID:    senderID,
		Text:        text,
		Done:        done,
	}
	<-done
}

// maxSeenMsgs caps the seenMsgs map to prevent unbounded memory growth.
const maxSeenMsgs = 10_000

// seenMsgsEvictionLoop periodically removes old entries from seenMsgs.
// Runs as a single goroutine started by Start(), stops when ctx is cancelled.
func (l *LarkChannel) seenMsgsEvictionLoop(
	ctx context.Context, maxAge time.Duration,
) {
	ticker := time.NewTicker(maxAge)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-maxAge)
			count := 0
			l.seenMsgs.Range(func(key, value any) bool {
				if ts, ok := value.(time.Time); ok && ts.Before(cutoff) {
					l.seenMsgs.Delete(key)
				} else {
					count++
				}
				return true
			})
			// If still over cap after age eviction, force-evict oldest.
			if count > maxSeenMsgs {
				evicted := 0
				l.seenMsgs.Range(func(key, _ any) bool {
					l.seenMsgs.Delete(key)
					evicted++
					return evicted < count-maxSeenMsgs
				})
			}
		}
	}
}

func truncateForLog(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}

// Send sends a message to the chat identified in msg.ChannelID.
// If the chat was already sent to during this cycle (e.g. by a tool call),
// the send is skipped to avoid duplicate replies.
func (l *LarkChannel) Send(
	ctx context.Context, msg channel.OutgoingMessage,
) error {
	if _, alreadySent := l.sentChats.LoadOrStore(msg.ChannelID, true); alreadySent {
		l.logger.Debug("skipping duplicate send", zap.String("chat_id", msg.ChannelID))
		return nil
	}
	return l.sendCard(ctx, msg.ChannelID, msg.Text)
}

// SendTyping is a no-op for Lark.
func (l *LarkChannel) SendTyping(_ context.Context, _ string) error { return nil }

// SupportsStreaming returns true; Lark updates cards progressively.
func (l *LarkChannel) SupportsStreaming() bool { return true }

// streamUpdateInterval controls how often the Lark card is patched during streaming.
const streamUpdateInterval = 800 * time.Millisecond

// SendStream sends an initial card and patches it as tokens arrive.
// Tokens are buffered and the card is updated at streamUpdateInterval.
// The typing cursor (▍) is appended during streaming and removed on
// the final update.
func (l *LarkChannel) SendStream(
	ctx context.Context, channelID string,
	tokenCh <-chan string,
) error {
	l.sentChats.Store(channelID, true)

	var sb strings.Builder
	var messageID string
	ticker := time.NewTicker(streamUpdateInterval)
	defer ticker.Stop()
	dirty := false

	for {
		select {
		case tok, ok := <-tokenCh:
			if !ok {
				// Stream done. Final update without cursor.
				if messageID != "" {
					l.patchCard(ctx, messageID, sb.String())
				} else if sb.Len() > 0 {
					l.sendCard(ctx, channelID, sb.String())
				}
				return nil
			}
			sb.WriteString(tok)
			dirty = true

		case <-ticker.C:
			if !dirty || sb.Len() == 0 {
				continue
			}
			content := sb.String() + " ▍"
			if messageID == "" {
				id, err := l.sendCardReturnID(ctx, channelID, content)
				if err != nil {
					l.logger.Warn("lark stream: failed to send initial card", zap.Error(err))
					continue
				}
				messageID = id
			} else {
				l.patchCard(ctx, messageID, content)
			}
			dirty = false

		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// SendToChat sends a message to a specific chat_id. Exported for use by tools.
// It records the chat_id so that a subsequent Send to the same chat is skipped.
func (l *LarkChannel) SendToChat(
	ctx context.Context, chatID, text string,
) error {
	l.sentChats.Store(chatID, true)

	return l.sendCard(ctx, chatID, text)
}

// sendCard sends a message as an interactive card with a markdown element.
func (l *LarkChannel) sendCard(
	ctx context.Context, chatID, text string,
) error {
	_, err := l.sendCardReturnID(ctx, chatID, text)
	return err
}

// buildCard returns a JSON-encoded Lark interactive card body.
func buildCard(text string) []byte {
	card := map[string]any{
		"elements": []map[string]string{
			{"tag": "markdown", "content": sanitizeLarkMarkdown(text)},
		},
	}
	content, _ := json.Marshal(card)
	return content
}

// sendCardReturnID sends a card and returns the message_id for later patching.
func (l *LarkChannel) sendCardReturnID(
	ctx context.Context, chatID, text string,
) (string, error) {
	l.logger.Info("sending lark card",
		zap.String("chat_id", chatID),
		zap.Int("text_len", len(text)))
	content := buildCard(text)

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
		return "", fmt.Errorf("lark send: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("lark send: code=%d msg=%s", resp.Code, resp.Msg)
	}

	var messageID string
	if resp.Data != nil && resp.Data.MessageId != nil {
		messageID = *resp.Data.MessageId
	}
	return messageID, nil
}

// patchCard updates an existing card message with new content.
func (l *LarkChannel) patchCard(
	ctx context.Context, messageID, text string,
) {
	content := buildCard(text)

	req := larkim.NewPatchMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewPatchMessageReqBodyBuilder().
			Content(string(content)).
			Build()).
		Build()

	resp, err := l.client.Im.V1.Message.Patch(ctx, req)
	if err != nil {
		l.logger.Debug("lark patch card error", zap.Error(err))
		return
	}
	if !resp.Success() {
		l.logger.Debug("lark patch card failed", zap.Int("code", resp.Code), zap.String("msg", resp.Msg))
	}
}

// ListChats returns the groups the bot is a member of (for discovering chat_ids).
func (l *LarkChannel) ListChats(
	ctx context.Context,
) ([]ChatInfo, error) {
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

// ReadDocContent returns the plain text content of a Feishu document.
func (l *LarkChannel) ReadDocContent(
	ctx context.Context, documentID string,
) (string, error) {
	req := larkdocx.NewRawContentDocumentReqBuilder().
		DocumentId(documentID).
		Lang(0).
		Build()

	resp, err := l.client.Docx.V1.Document.RawContent(ctx, req)
	if err != nil {
		return "", fmt.Errorf("lark read doc: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("lark read doc: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.Content == nil {
		return "", nil
	}
	return *resp.Data.Content, nil
}

// maxBlocksPerRequest is the Feishu API limit for creating blocks in one call.
const maxBlocksPerRequest = 50

// WriteDocContent replaces all content in a Feishu document with markdown content.
// It uses Document.Convert() to parse markdown into properly typed blocks (headings,
// bullets, code blocks, etc.), then replaces the document's children with those blocks.
func (l *LarkChannel) WriteDocContent(
	ctx context.Context, documentID, content string,
) error {
	// 1. Convert markdown content to Feishu blocks.
	convReq := larkdocx.NewConvertDocumentReqBuilder().
		Body(larkdocx.NewConvertDocumentReqBodyBuilder().
			ContentType(larkdocx.ContentTypeMarkdown).
			Content(content).
			Build()).
		Build()

	convResp, err := l.client.Docx.V1.Document.Convert(ctx, convReq)
	if err != nil {
		return fmt.Errorf("lark write doc: convert markdown: %w", err)
	}
	if !convResp.Success() {
		return fmt.Errorf("lark write doc: convert markdown: code=%d msg=%s", convResp.Code, convResp.Msg)
	}

	// Build a set of first-level block IDs for filtering.
	firstLevel := make(map[string]bool, len(convResp.Data.FirstLevelBlockIds))
	for _, id := range convResp.Data.FirstLevelBlockIds {
		firstLevel[id] = true
	}

	// Collect first-level blocks in order, clearing temporary IDs.
	var blocks []*larkdocx.Block
	for _, b := range convResp.Data.Blocks {
		if b.BlockId != nil && firstLevel[*b.BlockId] {
			b.BlockId = nil
			b.ParentId = nil
			b.Children = nil
			blocks = append(blocks, b)
		}
	}

	// 2. Get root block to find existing children count.
	getReq := larkdocx.NewGetDocumentBlockReqBuilder().
		DocumentId(documentID).
		BlockId(documentID).
		DocumentRevisionId(-1).
		Build()

	getResp, err := l.client.Docx.V1.DocumentBlock.Get(ctx, getReq)
	if err != nil {
		return fmt.Errorf("lark write doc: get root block: %w", err)
	}
	if !getResp.Success() {
		return fmt.Errorf("lark write doc: get root block: code=%d msg=%s", getResp.Code, getResp.Msg)
	}

	// 3. Delete all existing children.
	childCount := len(getResp.Data.Block.Children)
	if childCount > 0 {
		startIdx := 0
		endIdx := childCount
		delReq := larkdocx.NewBatchDeleteDocumentBlockChildrenReqBuilder().
			DocumentId(documentID).
			BlockId(documentID).
			DocumentRevisionId(-1).
			Body(&larkdocx.BatchDeleteDocumentBlockChildrenReqBody{
				StartIndex: &startIdx,
				EndIndex:   &endIdx,
			}).
			Build()

		delResp, err := l.client.Docx.V1.DocumentBlockChildren.BatchDelete(ctx, delReq)
		if err != nil {
			return fmt.Errorf("lark write doc: delete children: %w", err)
		}
		if !delResp.Success() {
			return fmt.Errorf("lark write doc: delete children: code=%d msg=%s", delResp.Code, delResp.Msg)
		}
	}

	// 4. Create converted blocks in batches of 50.
	for i := 0; i < len(blocks); i += maxBlocksPerRequest {
		end := i + maxBlocksPerRequest
		if end > len(blocks) {
			end = len(blocks)
		}
		chunk := blocks[i:end]

		insertIdx := i
		createReq := larkdocx.NewCreateDocumentBlockChildrenReqBuilder().
			DocumentId(documentID).
			BlockId(documentID).
			DocumentRevisionId(-1).
			Body(&larkdocx.CreateDocumentBlockChildrenReqBody{
				Children: chunk,
				Index:    &insertIdx,
			}).
			Build()

		createResp, err := l.client.Docx.V1.DocumentBlockChildren.Create(ctx, createReq)
		if err != nil {
			return fmt.Errorf("lark write doc: create blocks (batch %d-%d): %w", i, end, err)
		}
		if !createResp.Success() {
			return fmt.Errorf("lark write doc: create blocks (batch %d-%d): code=%d msg=%s", i, end, createResp.Code, createResp.Msg)
		}
	}

	return nil
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

// headerRe matches markdown header lines (e.g. "## Title").
var headerRe = regexp.MustCompile(`(?m)^(#{1,6})\s+(.+)$`)

// blockquoteRe matches markdown blockquote lines (e.g. "> text").
var blockquoteRe = regexp.MustCompile(`(?m)^>\s?(.*)$`)

// inlineCodeRe matches inline code (e.g. "`text`") but not fenced blocks.
var inlineCodeRe = regexp.MustCompile("`([^`]+)`")

// codeBlockRe splits text on fenced code blocks (``` … ```).
var codeBlockRe = regexp.MustCompile("(?s)(```.*?```)")

// sanitizeLarkMarkdown converts standard markdown into the subset that
// Lark card markdown supports. Unsupported elements:
//   - Headers (# … ######) → bold text
//   - Blockquotes (>) → italic text
//   - Inline code (`text`) → plain text (backticks stripped)
//
// Content inside fenced code blocks is left untouched.
func sanitizeLarkMarkdown(text string) string {
	parts := codeBlockRe.Split(text, -1)
	codeBlocks := codeBlockRe.FindAllString(text, -1)

	var sb strings.Builder
	for i, part := range parts {
		part = headerRe.ReplaceAllString(part, "**$2**")
		part = blockquoteRe.ReplaceAllString(part, "*$1*")
		part = inlineCodeRe.ReplaceAllString(part, "$1")
		sb.WriteString(part)
		if i < len(codeBlocks) {
			sb.WriteString(codeBlocks[i])
		}
	}
	return sb.String()
}

// zapLarkLogger adapts zap.Logger to the Lark SDK's Logger interface.
type zapLarkLogger struct {
	z *zap.Logger
}

func (l *zapLarkLogger) Debug(_ context.Context, args ...any) {
	l.z.Debug(fmt.Sprint(args...))
}

func (l *zapLarkLogger) Info(_ context.Context, args ...any) {
	l.z.Info(fmt.Sprint(args...))
}

func (l *zapLarkLogger) Warn(_ context.Context, args ...any) {
	l.z.Warn(fmt.Sprint(args...))
}

func (l *zapLarkLogger) Error(_ context.Context, args ...any) {
	l.z.Error(fmt.Sprint(args...))
}
