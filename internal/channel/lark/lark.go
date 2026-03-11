package lark

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
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
	"github.com/joechenrh/golem/internal/stringutil"
)

// CardActionCallback is called when a user clicks a button on a Lark card.
type CardActionCallback func(chatID string, action map[string]any)

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

	// pendingReactions tracks typing reaction IDs keyed by channelID,
	// so SendStream can remove the reaction when the response completes.
	// Values are reactionInfo structs.
	pendingReactions sync.Map

	// startedAt is the process startup time. Messages with a Lark
	// CreateTime before this are stale redeliveries from a previous
	// process and are dropped.
	startedAt time.Time

	// onCardAction is called when a user clicks a button on a card.
	// Set via SetCardActionHandler before Start.
	onCardAction CardActionCallback
}

// reactionInfo holds the IDs needed to remove a typing reaction.
type reactionInfo struct {
	messageID  string
	reactionID string
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

// SetCardActionHandler registers a callback for card button clicks.
func (l *LarkChannel) SetCardActionHandler(handler CardActionCallback) {
	l.onCardAction = handler
}

// CardActionHTTPHandler returns an http.HandlerFunc that processes
// Lark card action callbacks. Mount this on an HTTP server to enable
// interactive card buttons (reset session, feedback).
func (l *LarkChannel) CardActionHTTPHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		var payload struct {
			Challenge  string `json:"challenge"`
			OpenChatID string `json:"open_chat_id"`
			Action     struct {
				Value map[string]any `json:"value"`
			} `json:"action"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}

		// URL verification challenge.
		if payload.Challenge != "" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"challenge": payload.Challenge})
			return
		}

		if l.onCardAction != nil && payload.OpenChatID != "" && len(payload.Action.Value) > 0 {
			l.onCardAction(payload.OpenChatID, payload.Action.Value)
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{}"))
	}
}

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
	if msg == nil || msg.MessageType == nil {
		l.logger.Warn("lark event with nil message or message_type")
		return
	}
	msgType := *msg.MessageType
	if msgType != "text" && msgType != "image" && msgType != "post" {
		l.logger.Info("ignoring unsupported lark message type", zap.String("type", msgType))
		return
	}
	if msg.Content == nil || msg.ChatId == nil {
		l.logger.Warn("lark message missing content or chat_id",
			zap.String("type", msgType),
			zap.Bool("content_nil", msg.Content == nil),
			zap.Bool("chat_id_nil", msg.ChatId == nil))
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

	var text string
	var images []channel.ImageData

	switch msgType {
	case "text":
		text = extractTextContent(*msg.Content)
		if text == "" {
			return
		}
	case "image":
		imageKey := extractImageKey(*msg.Content)
		if imageKey == "" {
			l.logger.Warn("image message missing image_key", zap.String("message_id", msgID))
			return
		}
		imgData, mediaType, err := l.downloadImage(context.Background(), msgID, imageKey)
		if err != nil {
			l.logger.Error("failed to download lark image",
				zap.String("message_id", msgID), zap.Error(err))
			return
		}
		encoded := base64.StdEncoding.EncodeToString(imgData)
		images = append(images, channel.ImageData{
			Base64:    encoded,
			MediaType: mediaType,
		})
		text = "[User sent an image]"
	case "post":
		postText, imageKeys := extractPostContent(*msg.Content)
		for _, key := range imageKeys {
			imgData, mediaType, err := l.downloadImage(context.Background(), msgID, key)
			if err != nil {
				l.logger.Error("failed to download lark post image",
					zap.String("message_id", msgID),
					zap.String("image_key", key), zap.Error(err))
				continue
			}
			encoded := base64.StdEncoding.EncodeToString(imgData)
			images = append(images, channel.ImageData{
				Base64:    encoded,
				MediaType: mediaType,
			})
		}
		text = postText
		if text == "" && len(images) > 0 {
			text = "[User sent an image]"
		}
	}

	// Strip @bot mentions.
	if mentions := msg.Mentions; len(mentions) > 0 {
		for _, m := range mentions {
			if m.Key != nil {
				text = strings.ReplaceAll(text, *m.Key, "")
			}
		}
		text = strings.TrimSpace(text)
		if text == "" && len(images) == 0 {
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
		zap.String("type", msgType),
		zap.String("text", stringutil.Truncate(text, 80)))

	// Reset per-cycle duplicate tracking before dispatching.
	l.sentChats.Clear()

	// Add typing reaction to user's message to indicate processing.
	chatID := *msg.ChatId
	if msgID != "" {
		reactionID := l.addReaction(context.Background(), msgID, "Typing")
		if reactionID != "" {
			l.pendingReactions.Store(chatID, reactionInfo{
				messageID:  msgID,
				reactionID: reactionID,
			})
		}
	}

	done := make(chan struct{})
	inCh <- channel.IncomingMessage{
		ChannelID:   chatID,
		ChannelName: "lark",
		SenderID:    senderID,
		Text:        text,
		Images:      images,
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
	defer l.removePendingReaction(ctx, msg.ChannelID)
	return l.sendCard(ctx, msg.ChannelID, msg.Text)
}

// SendTyping is a no-op for Lark.
func (l *LarkChannel) SendTyping(_ context.Context, _ string) error { return nil }

// addReaction adds an emoji reaction to a message and returns the reaction ID.
func (l *LarkChannel) addReaction(ctx context.Context, messageID, emojiType string) string {
	req := larkim.NewCreateMessageReactionReqBuilder().
		MessageId(messageID).
		Body(larkim.NewCreateMessageReactionReqBodyBuilder().
			ReactionType(larkim.NewEmojiBuilder().EmojiType(emojiType).Build()).
			Build()).
		Build()

	resp, err := l.client.Im.V1.MessageReaction.Create(ctx, req)
	if err != nil {
		l.logger.Debug("lark add reaction error", zap.Error(err))
		return ""
	}
	if !resp.Success() {
		l.logger.Debug("lark add reaction failed", zap.Int("code", resp.Code), zap.String("msg", resp.Msg))
		return ""
	}
	if resp.Data != nil && resp.Data.ReactionId != nil {
		return *resp.Data.ReactionId
	}
	return ""
}

// deleteReaction removes a reaction from a message.
func (l *LarkChannel) deleteReaction(ctx context.Context, messageID, reactionID string) {
	req := larkim.NewDeleteMessageReactionReqBuilder().
		MessageId(messageID).
		ReactionId(reactionID).
		Build()

	resp, err := l.client.Im.V1.MessageReaction.Delete(ctx, req)
	if err != nil {
		l.logger.Debug("lark delete reaction error", zap.Error(err))
		return
	}
	if !resp.Success() {
		l.logger.Debug("lark delete reaction failed", zap.Int("code", resp.Code), zap.String("msg", resp.Msg))
	}
}

// SupportsStreaming returns true; Lark updates cards progressively.
func (l *LarkChannel) SupportsStreaming() bool { return true }

// streamUpdateInterval controls how often the Lark card is patched during streaming.
const streamUpdateInterval = 800 * time.Millisecond

// SendStream streams tokens into a Lark card. The card is only created once
// actual content arrives (no "Thinking..." placeholder). A typing reaction on
// the user's message (added in onMessageReceive) signals that processing is
// in progress; it is removed when the stream completes.
func (l *LarkChannel) SendStream(
	ctx context.Context, channelID string,
	tokenCh <-chan string,
) error {
	l.sentChats.Store(channelID, true)
	err := channel.RunEditStream(ctx, &larkEditStreamer{ch: l, start: time.Now()}, channelID, tokenCh, streamUpdateInterval, " ▍")
	l.removePendingReaction(ctx, channelID)
	return err
}

// larkEditStreamer adapts LarkChannel to the channel.EditStreamer interface.
type larkEditStreamer struct {
	ch    *LarkChannel
	start time.Time // captured at construction for elapsed-time footer
}

func (s *larkEditStreamer) CreateMessage(ctx context.Context, channelID, text string) (string, error) {
	return s.ch.sendCardReturnID(ctx, channelID, text)
}

func (s *larkEditStreamer) UpdateMessage(ctx context.Context, _, messageID, text string) error {
	s.ch.patchCard(ctx, messageID, text)
	return nil
}

func (s *larkEditStreamer) FinalizeMessage(ctx context.Context, channelID, messageID, text string) error {
	if text == "" {
		s.ch.patchCard(ctx, messageID, "...")
		return nil
	}
	elapsed := time.Since(s.start)
	footer := fmt.Sprintf("%.1fs", elapsed.Seconds())
	s.ch.patchCardRaw(ctx, messageID, s.ch.buildCardWithImagesAndFooter(ctx, text, footer))
	return nil
}

// removePendingReaction removes the typing reaction stored for a channelID.
func (l *LarkChannel) removePendingReaction(ctx context.Context, channelID string) {
	val, ok := l.pendingReactions.LoadAndDelete(channelID)
	if !ok {
		return
	}
	info := val.(reactionInfo)
	l.deleteReaction(ctx, info.messageID, info.reactionID)
}

// SendDirect sends a message unconditionally to a specific chat_id.
// It records the chat_id so that a subsequent Send to the same chat is skipped.
// If the text contains markdown image references, they are uploaded and rendered
// as native Lark image elements.
func (l *LarkChannel) SendDirect(
	ctx context.Context, chatID, text string,
) error {
	l.sentChats.Store(chatID, true)

	cardJSON := l.buildCardWithImages(ctx, text)
	l.sendCardRaw(ctx, chatID, cardJSON)
	return nil
}

// sendCard sends a message as a structured interactive card.
func (l *LarkChannel) sendCard(
	ctx context.Context, chatID, text string,
) error {
	l.sendCardRaw(ctx, chatID, buildStructuredCard(text))
	return nil
}

// actionButtons is the shared action row appended to response cards.
// It contains a "New conversation" button and thumbs up/down feedback buttons.
var actionButtons = map[string]any{
	"tag": "action",
	"actions": []map[string]any{
		{
			"tag":  "button",
			"text": map[string]string{"tag": "plain_text", "content": "New conversation"},
			"type": "default",
			"value": map[string]string{
				"action": "reset_session",
			},
		},
		{
			"tag":  "button",
			"text": map[string]string{"tag": "plain_text", "content": "\U0001F44D"},
			"type": "default",
			"value": map[string]string{
				"action": "feedback",
				"value":  "up",
			},
		},
		{
			"tag":  "button",
			"text": map[string]string{"tag": "plain_text", "content": "\U0001F44E"},
			"type": "default",
			"value": map[string]string{
				"action": "feedback",
				"value":  "down",
			},
		},
	},
}

// buildCard returns a JSON-encoded Lark interactive card body.
func buildCard(text string) []byte {
	card := map[string]any{
		"elements": []any{
			map[string]string{"tag": "markdown", "content": sanitizeLarkMarkdown(text)},
			actionButtons,
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

// markdownImageRe matches markdown image references: ![alt](url)
var markdownImageRe = regexp.MustCompile(`!\[[^\]]*\]\(([^)]+)\)`)

// buildCardWithImages builds a card JSON that handles markdown images.
// It extracts ![alt](url) references, downloads and uploads each image
// to Lark, and returns a card with interleaved markdown + img elements.
// If no images are found or all uploads fail, falls back to a plain card.
func (l *LarkChannel) buildCardWithImages(ctx context.Context, text string) []byte {
	return l.buildCardWithImagesAndFooter(ctx, text, "")
}

func (l *LarkChannel) buildCardWithImagesAndFooter(ctx context.Context, text, footer string) []byte {
	matches := markdownImageRe.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return buildStructuredCardWithFooter(text, footer)
	}

	// Upload images in parallel, collecting image_keys indexed by match position.
	imageKeys := make([]string, len(matches))
	var wg sync.WaitGroup
	for i, m := range matches {
		urlStart, urlEnd := m[2], m[3]
		imgURL := text[urlStart:urlEnd]
		wg.Add(1)
		go func() {
			defer wg.Done()
			key, err := l.downloadAndUpload(ctx, imgURL)
			if err != nil {
				l.logger.Debug("image upload failed", zap.String("url", imgURL), zap.Error(err))
				return
			}
			imageKeys[i] = key
		}()
	}
	wg.Wait()

	// Build interleaved elements: markdown text segments + img elements.
	var elements []any
	prev := 0
	for i, m := range matches {
		matchStart, matchEnd := m[0], m[1]

		// Add markdown segment before this image.
		if prev < matchStart {
			segment := strings.TrimSpace(text[prev:matchStart])
			if segment != "" {
				elements = append(elements, map[string]any{
					"tag": "markdown", "content": sanitizeLarkMarkdown(segment),
				})
			}
		}

		if imageKeys[i] != "" {
			elements = append(elements, map[string]any{
				"tag":           "img",
				"img_key":       imageKeys[i],
				"alt":           map[string]string{"tag": "plain_text", "content": "image"},
				"mode":          "fit_horizontal",
				"compact_width": false,
			})
		}
		prev = matchEnd
	}

	// Add trailing text after the last image.
	if prev < len(text) {
		segment := strings.TrimSpace(text[prev:])
		if segment != "" {
			elements = append(elements, map[string]any{
				"tag": "markdown", "content": sanitizeLarkMarkdown(segment),
			})
		}
	}

	if len(elements) == 0 {
		return buildStructuredCardWithFooter(text, footer)
	}

	if footer != "" {
		elements = append(elements, map[string]any{
			"tag": "note",
			"elements": []any{
				map[string]any{"tag": "plain_text", "content": footer},
			},
		})
	}
	elements = append(elements, actionButtons)
	card := map[string]any{"elements": elements}
	content, _ := json.Marshal(card)
	return content
}

// downloadAndUpload fetches an image URL and uploads it to Lark, returning the image_key.
func (l *LarkChannel) downloadAndUpload(ctx context.Context, imgURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", imgURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return l.UploadImage(ctx, resp.Body)
}

// sendCardRaw sends a pre-built card JSON payload.
func (l *LarkChannel) sendCardRaw(ctx context.Context, chatID string, cardJSON []byte) {
	l.logger.Info("sending lark card",
		zap.String("chat_id", chatID),
		zap.Int("card_len", len(cardJSON)))

	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(
			larkim.NewCreateMessageReqBodyBuilder().
				ReceiveId(chatID).
				MsgType("interactive").
				Content(string(cardJSON)).
				Build(),
		).
		Build()

	resp, err := l.client.Im.V1.Message.Create(ctx, req)
	if err != nil {
		l.logger.Debug("lark send card error", zap.Error(err))
		return
	}
	if !resp.Success() {
		l.logger.Debug("lark send card failed", zap.Int("code", resp.Code), zap.String("msg", resp.Msg))
	}
}

// patchCardRaw updates an existing card message with pre-built card JSON.
func (l *LarkChannel) patchCardRaw(ctx context.Context, messageID string, cardJSON []byte) {
	req := larkim.NewPatchMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewPatchMessageReqBodyBuilder().
			Content(string(cardJSON)).
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

// ChatMessage represents a single message from a Lark chat history.
type ChatMessage struct {
	SenderID   string // open_id or app_id
	SenderType string // "user" or "app"
	MsgType    string // "text", "post", "image", etc.
	Content    string // extracted text content
	CreateTime string // millisecond timestamp
}

// ListMessages returns recent messages from a Lark chat, sorted oldest-first.
func (l *LarkChannel) ListMessages(
	ctx context.Context, chatID string, count int,
) ([]ChatMessage, error) {
	if count <= 0 {
		count = 20
	}
	if count > 50 {
		count = 50
	}

	req := larkim.NewListMessageReqBuilder().
		ContainerIdType("chat").
		ContainerId(chatID).
		PageSize(count).
		SortType("ByCreateTimeDesc").
		Build()

	resp, err := l.client.Im.V1.Message.List(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("lark list messages: %w", err)
	}
	if !resp.Success() {
		return nil, fmt.Errorf("lark list messages: code=%d msg=%s", resp.Code, resp.Msg)
	}

	var msgs []ChatMessage
	for _, item := range resp.Data.Items {
		cm := ChatMessage{}
		if item.MsgType != nil {
			cm.MsgType = *item.MsgType
		}
		if item.CreateTime != nil {
			cm.CreateTime = *item.CreateTime
		}
		if item.Sender != nil {
			if item.Sender.Id != nil {
				cm.SenderID = *item.Sender.Id
			}
			if item.Sender.SenderType != nil {
				cm.SenderType = *item.Sender.SenderType
			}
		}

		// Extract text content based on message type.
		if item.Body != nil && item.Body.Content != nil {
			raw := *item.Body.Content
			switch cm.MsgType {
			case "text":
				var tc struct {
					Text string `json:"text"`
				}
				if json.Unmarshal([]byte(raw), &tc) == nil {
					cm.Content = tc.Text
				}
			case "post":
				text, _ := extractPostContent(raw)
				cm.Content = text
			default:
				cm.Content = "[" + cm.MsgType + "]"
			}
		}

		msgs = append(msgs, cm)
	}

	// Reverse to oldest-first order.
	slices.Reverse(msgs)

	return msgs, nil
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

// extractPostContent parses a Lark rich-text (post) message and returns the
// combined text and any image keys found. The content may be either:
//
//	Locale-wrapped: {"zh_cn":{"title":"…","content":[[{"tag":"text","text":"…"}]]}}
//	Direct:         {"title":"…","content":[[{"tag":"text","text":"…"}]]}
func extractPostContent(content string) (text string, imageKeys []string) {
	type node struct {
		Tag      string `json:"tag"`
		Text     string `json:"text"`
		ImageKey string `json:"image_key"`
	}
	type post struct {
		Title   string   `json:"title"`
		Content [][]node `json:"content"`
	}

	flatten := func(p post) (string, []string) {
		var sb strings.Builder
		var keys []string
		if p.Title != "" {
			sb.WriteString(p.Title)
			sb.WriteString("\n")
		}
		for _, line := range p.Content {
			for _, n := range line {
				switch n.Tag {
				case "text":
					sb.WriteString(n.Text)
				case "img":
					if n.ImageKey != "" {
						keys = append(keys, n.ImageKey)
					}
				}
			}
		}
		return strings.TrimSpace(sb.String()), keys
	}

	// Try direct post format first.
	var direct post
	if err := json.Unmarshal([]byte(content), &direct); err == nil && len(direct.Content) > 0 {
		text, imageKeys = flatten(direct)
		if text != "" || len(imageKeys) > 0 {
			return text, imageKeys
		}
	}

	// Try locale-wrapped format.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return "", nil
	}
	for _, locale := range raw {
		var p post
		if err := json.Unmarshal(locale, &p); err != nil {
			continue
		}
		text, imageKeys = flatten(p)
		if text != "" || len(imageKeys) > 0 {
			return text, imageKeys
		}
	}
	return "", nil
}

// extractImageKey parses the Lark image message content JSON and returns the image_key.
// Lark image messages have the format: {"image_key":"img_xxx"}.
func extractImageKey(content string) string {
	var parsed struct {
		ImageKey string `json:"image_key"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return ""
	}
	return parsed.ImageKey
}

// downloadImage downloads an image from a Lark message using the message resource API.
func (l *LarkChannel) downloadImage(
	ctx context.Context, messageID, imageKey string,
) ([]byte, string, error) {
	req := larkim.NewGetMessageResourceReqBuilder().
		MessageId(messageID).
		FileKey(imageKey).
		Type("image").
		Build()

	resp, err := l.client.Im.V1.MessageResource.Get(ctx, req)
	if err != nil {
		return nil, "", fmt.Errorf("lark download image: %w", err)
	}
	if !resp.Success() {
		return nil, "", fmt.Errorf("lark download image: code=%d msg=%s", resp.Code, resp.Msg)
	}

	data, err := io.ReadAll(resp.File)
	if err != nil {
		return nil, "", fmt.Errorf("lark download image: read body: %w", err)
	}

	mediaType := http.DetectContentType(data)
	return data, mediaType, nil
}

// UploadImage uploads image data to Lark and returns the image_key.
func (l *LarkChannel) UploadImage(
	ctx context.Context, imageData io.Reader,
) (string, error) {
	req := larkim.NewCreateImageReqBuilder().
		Body(larkim.NewCreateImageReqBodyBuilder().
			ImageType("message").
			Image(imageData).
			Build()).
		Build()

	resp, err := l.client.Im.V1.Image.Create(ctx, req)
	if err != nil {
		return "", fmt.Errorf("lark upload image: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("lark upload image: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.ImageKey == nil {
		return "", fmt.Errorf("lark upload image: no image_key in response")
	}
	return *resp.Data.ImageKey, nil
}

// SendError sends a user-facing error card with a red header.
func (l *LarkChannel) SendError(ctx context.Context, chatID, text string) error {
	card := map[string]any{
		"header": map[string]any{
			"template": "red",
			"title": map[string]string{
				"tag":     "plain_text",
				"content": "Error",
			},
		},
		"elements": []map[string]string{
			{"tag": "markdown", "content": text},
		},
	}
	content, _ := json.Marshal(card)
	l.sendCardRaw(ctx, chatID, content)
	return nil
}

// SendImageToChat sends an image message to a Lark chat.
func (l *LarkChannel) SendImageToChat(
	ctx context.Context, chatID, imageKey string,
) error {
	l.sentChats.Store(chatID, true)

	content, _ := json.Marshal(map[string]string{"image_key": imageKey})
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType("image").
			Content(string(content)).
			Build()).
		Build()

	resp, err := l.client.Im.V1.Message.Create(ctx, req)
	if err != nil {
		return fmt.Errorf("lark send image: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("lark send image: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
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
