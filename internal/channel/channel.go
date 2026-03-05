package channel

import "context"

// IncomingMessage represents a message received from a channel.
type IncomingMessage struct {
	ChannelID   string // e.g. "cli", "telegram:12345", "lark:chat_xyz"
	ChannelName string // "cli", "telegram", "lark"
	SenderID    string
	SenderName  string
	Text        string
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
	Send(ctx context.Context, msg OutgoingMessage) error
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
