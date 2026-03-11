package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/joechenrh/golem/internal/hooks"
)

// DirectSender is the subset of channel.Channel needed by ProgressReporter.
// Defined here to avoid importing the channel package.
type DirectSender interface {
	SendDirect(ctx context.Context, chatID, text string) error
}

// ProgressReporter implements hooks.Hook and sends milestone updates
// to a chat channel when phase_update events are emitted.
type ProgressReporter struct {
	channel  DirectSender
	chatID   string
	minGap   time.Duration
	mu       sync.Mutex
	lastSent time.Time
	logger   *zap.Logger
}

// NewProgressReporter creates a reporter that sends milestone updates
// to the given channel/chatID. minGap is the minimum time between messages
// (use 0 to disable throttling, e.g. in tests).
func NewProgressReporter(
	ch DirectSender, chatID string, minGap time.Duration, logger *zap.Logger,
) *ProgressReporter {
	return &ProgressReporter{
		channel: ch,
		chatID:  chatID,
		minGap:  minGap,
		logger:  logger,
	}
}

// Name implements hooks.Hook.
func (r *ProgressReporter) Name() string { return "progress_reporter" }

// Handle implements hooks.Hook. Only acts on phase_update events.
func (r *ProgressReporter) Handle(ctx context.Context, event hooks.Event) error {
	if event.Type != hooks.EventPhaseUpdate {
		return nil
	}

	r.mu.Lock()
	if r.minGap > 0 && time.Since(r.lastSent) < r.minGap {
		r.mu.Unlock()
		return nil
	}
	r.lastSent = time.Now()
	r.mu.Unlock()

	summary, _ := event.Payload["summary"].(string)
	if err := r.channel.SendDirect(ctx, r.chatID, fmt.Sprintf("📋 %s", summary)); err != nil {
		r.logger.Warn("failed to send progress update",
			zap.String("chat_id", r.chatID), zap.Error(err))
	}
	return nil
}
