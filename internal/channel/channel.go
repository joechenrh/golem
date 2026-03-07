package channel

import "context"

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
