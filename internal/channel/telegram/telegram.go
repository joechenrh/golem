package telegram

import (
	"context"
	"fmt"

	"github.com/joechenrh/golem/internal/channel"
)

// TelegramChannel is a stub for future Telegram bot integration.
// Embeds BaseChannel for default no-op methods.
type TelegramChannel struct {
	channel.BaseChannel
}

// New creates a TelegramChannel stub.
func New() *TelegramChannel {
	return &TelegramChannel{
		BaseChannel: channel.BaseChannel{ChannelName: "telegram"},
	}
}

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
