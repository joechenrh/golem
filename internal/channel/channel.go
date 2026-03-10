package channel

import (
	"context"
	"strings"
	"time"
)

// ImageData holds a downloaded image for multimodal messages.
type ImageData struct {
	Base64    string
	MediaType string
}

// IncomingMessage represents a message received from a channel.
type IncomingMessage struct {
	ChannelID   string // e.g. "cli", "telegram:12345", "lark:chat_xyz"
	ChannelName string // "cli", "telegram", "lark"
	SenderID    string
	SenderName  string
	Text        string
	Images      []ImageData
	Metadata    map[string]string
	Done        chan struct{} // closed when processing is complete (optional)
}

// OutgoingMessage represents a message to send to a channel.
type OutgoingMessage struct {
	ChannelID string
	Text      string
	Format    string // "text", "markdown"
}

// Channel is the interface for message I/O adapters.
type Channel interface {
	Name() string
	Start(ctx context.Context, inCh chan<- IncomingMessage) error

	// Send delivers a response to the current message. Implementations may
	// deduplicate: e.g. Lark skips if the chat was already replied to in
	// this processing cycle.
	Send(ctx context.Context, msg OutgoingMessage) error

	// SendDirect delivers a message unconditionally — no deduplication.
	// Used by tools, slash commands, scheduler, and card action handlers.
	SendDirect(ctx context.Context, channelID, text string) error

	// SendError delivers a user-facing error message. Implementations
	// should style it distinctly (e.g. red card header in Lark, red text
	// in CLI).
	SendError(ctx context.Context, channelID, text string) error

	SendTyping(ctx context.Context, channelID string) error
	SupportsStreaming() bool
	SendStream(ctx context.Context, channelID string, tokenCh <-chan string) error
}

// SystemPrinter provides formatted output for system-level messages.
// Implemented by channels that support direct user-facing output (e.g. CLI).
type SystemPrinter interface {
	PrintSystem(text string)
	PrintError(text string)
	PrintBanner(model string, toolCount int, tapePath string)
}

// channelIDKey is the context key for the current channel ID.
type channelIDKey struct{}

// WithChannelID returns a context carrying the channel ID so that
// downstream tools can discover which chat they are operating in.
func WithChannelID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, channelIDKey{}, id)
}

// ChannelIDFromContext extracts the channel ID from a context.
func ChannelIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(channelIDKey{}).(string); ok {
		return v
	}
	return ""
}

// BaseChannel provides no-op defaults for optional Channel methods.
// Embed this in channel implementations to reduce boilerplate — only
// override Start, Send, and SendDirect (the essentials).
type BaseChannel struct {
	ChannelName string
}

func (b *BaseChannel) Name() string                                   { return b.ChannelName }
func (b *BaseChannel) SendTyping(_ context.Context, _ string) error   { return nil }
func (b *BaseChannel) SendError(_ context.Context, _, _ string) error { return nil }
func (b *BaseChannel) SupportsStreaming() bool                        { return false }
func (b *BaseChannel) SendStream(_ context.Context, _ string, tokenCh <-chan string) error {
	// Drain to prevent goroutine leaks when streaming is not supported.
	for range tokenCh {
	}
	return nil
}

// CollectStream drains a token channel and returns the concatenated text.
// Useful for channels that want to buffer a stream into a single Send call.
func CollectStream(tokenCh <-chan string) string {
	var buf strings.Builder
	for tok := range tokenCh {
		buf.WriteString(tok)
	}
	return buf.String()
}

// EditStreamer is implemented by channels that support streaming via
// message editing (create → update → finalize). Used with RunEditStream.
type EditStreamer interface {
	// CreateMessage sends an initial message and returns its ID for later edits.
	CreateMessage(ctx context.Context, channelID, text string) (messageID string, err error)
	// UpdateMessage edits an existing message in place.
	UpdateMessage(ctx context.Context, channelID, messageID, text string) error
	// FinalizeMessage performs the final edit (e.g. image upload, rich formatting).
	FinalizeMessage(ctx context.Context, channelID, messageID, text string) error
}

// RunEditStream implements streaming by periodically editing a message.
// It reads tokens from tokenCh, creates a message on first content, updates
// at the given interval, and calls FinalizeMessage when the stream ends.
// The cursor string (e.g. " ▍") is appended during intermediate updates.
func RunEditStream(
	ctx context.Context, es EditStreamer,
	channelID string, tokenCh <-chan string,
	interval time.Duration, cursor string,
) error {
	var messageID string
	var buf strings.Builder
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	dirty := false

	for {
		select {
		case tok, ok := <-tokenCh:
			if !ok {
				// Stream complete — finalize.
				text := buf.String()
				if messageID != "" {
					return es.FinalizeMessage(ctx, channelID, messageID, text)
				}
				if text != "" {
					// Never got a tick; create and finalize in one shot.
					id, err := es.CreateMessage(ctx, channelID, text)
					if err != nil {
						return err
					}
					return es.FinalizeMessage(ctx, channelID, id, text)
				}
				return nil
			}
			buf.WriteString(tok)
			dirty = true

		case <-ticker.C:
			if !dirty || buf.Len() == 0 {
				continue
			}
			content := buf.String() + cursor
			if messageID == "" {
				id, err := es.CreateMessage(ctx, channelID, content)
				if err != nil {
					continue // retry on next tick
				}
				messageID = id
			} else {
				es.UpdateMessage(ctx, channelID, messageID, content)
			}
			dirty = false

		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
