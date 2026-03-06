package scheduler

import "time"

// Schedule represents a scheduled task that fires a prompt on a cron schedule.
type Schedule struct {
	ID          string    `json:"id"`
	CronExpr    string    `json:"cron_expr"`
	Prompt      string    `json:"prompt"`
	ChannelName string    `json:"channel_name"`
	ChannelID   string    `json:"channel_id"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	LastFiredAt time.Time `json:"last_fired_at,omitempty"`
	Enabled     bool      `json:"enabled"`
}
