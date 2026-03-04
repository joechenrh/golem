package lark

import (
	"context"
	"fmt"

	"github.com/joechenrh/golem/internal/channel"
)

// LarkChannel is a stub for future Lark/Feishu bot integration.
type LarkChannel struct{}

// New creates a LarkChannel stub.
func New() *LarkChannel { return &LarkChannel{} }

func (l *LarkChannel) Name() string { return "lark" }

func (l *LarkChannel) Start(_ context.Context, _ chan<- channel.IncomingMessage) error {
	return fmt.Errorf("lark channel not implemented")
}

func (l *LarkChannel) Send(_ context.Context, _ channel.OutgoingMessage) error { return nil }

func (l *LarkChannel) SendTyping(_ context.Context, _ string) error { return nil }

func (l *LarkChannel) SupportsStreaming() bool { return false }

func (l *LarkChannel) SendStream(_ context.Context, _ string, _ <-chan string) error {
	return nil
}
