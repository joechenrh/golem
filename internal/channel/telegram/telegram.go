package telegram

import (
	"context"
	"fmt"

	"github.com/joechenrh/golem/internal/channel"
)

// TelegramChannel is a stub for future Telegram bot integration.
type TelegramChannel struct{}

// New creates a TelegramChannel stub.
func New() *TelegramChannel { return &TelegramChannel{} }

func (t *TelegramChannel) Name() string { return "telegram" }

func (t *TelegramChannel) Start(
	_ context.Context,
	_ chan<- channel.IncomingMessage,
) error {
	return fmt.Errorf("telegram channel not implemented")
}

func (t *TelegramChannel) Send(
	_ context.Context, _ channel.OutgoingMessage,
) error {
	return nil
}

func (t *TelegramChannel) SendDirect(_ context.Context, _, _ string) error { return nil }
func (t *TelegramChannel) SendError(_ context.Context, _, _ string) error  { return nil }
func (t *TelegramChannel) SendTyping(_ context.Context, _ string) error    { return nil }

func (t *TelegramChannel) SupportsStreaming() bool { return false }

func (t *TelegramChannel) SendStream(
	_ context.Context, _ string, _ <-chan string,
) error {
	return nil
}
