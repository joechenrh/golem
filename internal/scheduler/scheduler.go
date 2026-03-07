package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
	"go.uber.org/zap"

	"github.com/joechenrh/golem/internal/channel"
)

// SessionFactory creates isolated sessions for scheduled task execution.
type SessionFactory interface {
	// HandleScheduledPrompt creates an ephemeral session, runs the prompt
	// through it, and returns the final response.
	HandleScheduledPrompt(ctx context.Context, tapePath string, msg channel.IncomingMessage) (string, error)
}

// Scheduler runs a background tick loop that checks cron schedules and fires
// prompts through isolated sessions.
type Scheduler struct {
	store     *Store
	channels  map[string]channel.Channel
	factory   SessionFactory
	logger    *zap.Logger
	cronCache map[string]cron.Schedule
	parser    cron.Parser
}

// New creates a Scheduler. Call Run() to start the tick loop.
func New(
	store *Store,
	channels map[string]channel.Channel,
	factory SessionFactory,
	logger *zap.Logger,
) *Scheduler {
	s := &Scheduler{
		store:     store,
		channels:  channels,
		factory:   factory,
		logger:    logger.Named("scheduler"),
		cronCache: make(map[string]cron.Schedule),
		parser:    cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor),
	}
	s.rebuildCache()
	return s
}

// Run starts the tick loop, checking every 60 seconds. Blocks until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	s.logger.Info("scheduler started")
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	// Do an immediate check on startup.
	s.tick(ctx)

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("scheduler stopped")
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

// RebuildCache parses all schedule cron expressions and caches them.
func (s *Scheduler) rebuildCache() {
	for _, sched := range s.store.List() {
		if _, ok := s.cronCache[sched.ID]; ok {
			continue
		}
		cs, err := s.parser.Parse(sched.CronExpr)
		if err != nil {
			s.logger.Error("invalid cron expression in stored schedule",
				zap.String("id", sched.ID), zap.String("cron", sched.CronExpr), zap.Error(err))
			continue
		}
		s.cronCache[sched.ID] = cs
	}
}

// InvalidateCache removes a schedule from the cron cache.
func (s *Scheduler) InvalidateCache(id string) {
	delete(s.cronCache, id)
}

// AddToCache parses and caches a cron expression for a schedule ID.
func (s *Scheduler) AddToCache(id, cronExpr string) {
	cs, err := s.parser.Parse(cronExpr)
	if err != nil {
		s.logger.Error("failed to cache cron expression",
			zap.String("id", id), zap.String("cron", cronExpr), zap.Error(err))
		return
	}
	s.cronCache[id] = cs
}

func (s *Scheduler) tick(ctx context.Context) {
	now := time.Now()
	schedules := s.store.List()

	// Rebuild cache for any new schedules (e.g., added via tools since last tick).
	s.rebuildCache()

	for _, sched := range schedules {
		if !sched.Enabled {
			continue
		}

		cs, ok := s.cronCache[sched.ID]
		if !ok {
			continue
		}

		// Determine the reference time for Next().
		ref := sched.LastFiredAt
		if ref.IsZero() {
			ref = sched.CreatedAt
		}

		next := cs.Next(ref)
		if !next.After(now) {
			s.fire(ctx, sched)
		}
	}
}

func (s *Scheduler) fire(ctx context.Context, sched Schedule) {
	s.logger.Info("firing scheduled task",
		zap.String("id", sched.ID), zap.String("desc", sched.Description))

	tapePath := fmt.Sprintf("sched-%s-%s.jsonl", sched.ID[:8], time.Now().Format("20060102-150405"))

	msg := channel.IncomingMessage{
		ChannelName: "scheduler",
		ChannelID:   sched.ChannelID,
		SenderID:    "scheduler",
		SenderName:  fmt.Sprintf("Scheduled: %s", sched.Description),
		Text:        sched.Prompt,
	}

	response, err := s.factory.HandleScheduledPrompt(ctx, tapePath, msg)

	var text string
	if err != nil {
		text = fmt.Sprintf("[Scheduled task %q failed]\n%s", sched.Description, err.Error())
	} else {
		text = response
	}

	ch, ok := s.channels[sched.ChannelName]
	if !ok {
		s.logger.Error("schedule target channel not found",
			zap.String("channel_name", sched.ChannelName), zap.String("id", sched.ID))
	} else if err := ch.SendDirect(ctx, sched.ChannelID, text); err != nil {
		s.logger.Error("failed to deliver scheduled message", zap.Error(err))
	}

	s.store.UpdateLastFired(sched.ID, time.Now())
}
