package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/joechenrh/golem/internal/scheduler"
)

// --- schedule_add ---

type ScheduleAddTool struct {
	store *scheduler.Store
	sched *scheduler.Scheduler
}

func NewScheduleAddTool(store *scheduler.Store, sched *scheduler.Scheduler) *ScheduleAddTool {
	return &ScheduleAddTool{store: store, sched: sched}
}

func (t *ScheduleAddTool) Name() string        { return "schedule_add" }
func (t *ScheduleAddTool) Description() string { return "Add a scheduled task" }
func (t *ScheduleAddTool) FullDescription() string {
	return "Add a scheduled task that fires a prompt on a cron schedule. " +
		"The prompt is sent to the agent in an isolated session at each fire time, " +
		"and the agent's response is posted to the target channel. " +
		"Supports standard cron expressions (\"0 9 * * 1-5\"), descriptors (\"@daily\", \"@hourly\"), " +
		"and intervals (\"@every 30m\"). Timezone prefix supported: \"CRON_TZ=Asia/Shanghai 0 9 * * *\"."
}

var scheduleAddParams = json.RawMessage(`{
	"type": "object",
	"properties": {
		"cron_expr": {
			"type": "string",
			"description": "Cron expression. Examples: \"0 9 * * 1-5\" (weekdays 9am), \"@daily\", \"@every 30m\""
		},
		"prompt": {
			"type": "string",
			"description": "The prompt sent to the agent when this schedule fires"
		},
		"channel_name": {
			"type": "string",
			"description": "Target channel type (e.g. \"lark\")"
		},
		"channel_id": {
			"type": "string",
			"description": "Target channel/chat ID (e.g. \"oc_xxx\")"
		},
		"description": {
			"type": "string",
			"description": "Human-friendly label for this schedule"
		}
	},
	"required": ["cron_expr", "prompt", "channel_name", "channel_id"]
}`)

func (t *ScheduleAddTool) Parameters() json.RawMessage { return scheduleAddParams }

func (t *ScheduleAddTool) Execute(_ context.Context, args string) (string, error) {
	var params struct {
		CronExpr    string `json:"cron_expr"`
		Prompt      string `json:"prompt"`
		ChannelName string `json:"channel_name"`
		ChannelID   string `json:"channel_id"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "Error: invalid arguments: " + err.Error(), nil
	}
	if params.CronExpr == "" {
		return "Error: 'cron_expr' is required", nil
	}
	if params.Prompt == "" {
		return "Error: 'prompt' is required", nil
	}
	if params.ChannelName == "" {
		return "Error: 'channel_name' is required", nil
	}
	if params.ChannelID == "" {
		return "Error: 'channel_id' is required", nil
	}

	id, err := t.store.Add(params.CronExpr, params.Prompt, params.ChannelName, params.ChannelID, params.Description)
	if err != nil {
		return "Error: " + err.Error(), nil
	}

	// Update the scheduler's cron cache if available.
	if t.sched != nil {
		t.sched.AddToCache(id, params.CronExpr)
	}

	return fmt.Sprintf("Schedule created: %s\nID: %s\nCron: %s\nTarget: %s/%s",
		params.Description, id, params.CronExpr, params.ChannelName, params.ChannelID), nil
}

// --- schedule_list ---

type ScheduleListTool struct {
	store *scheduler.Store
}

func NewScheduleListTool(store *scheduler.Store) *ScheduleListTool {
	return &ScheduleListTool{store: store}
}

func (t *ScheduleListTool) Name() string        { return "schedule_list" }
func (t *ScheduleListTool) Description() string { return "List scheduled tasks" }
func (t *ScheduleListTool) FullDescription() string {
	return "List all scheduled tasks with their ID, description, cron expression, " +
		"target channel, enabled status, and last/next fire times."
}

var scheduleListParams = json.RawMessage(`{"type":"object","properties":{}}`)

func (t *ScheduleListTool) Parameters() json.RawMessage { return scheduleListParams }

func (t *ScheduleListTool) Execute(_ context.Context, _ string) (string, error) {
	schedules := t.store.List()
	if len(schedules) == 0 {
		return "No scheduled tasks.", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Scheduled tasks (%d):\n\n", len(schedules))
	for _, s := range schedules {
		status := "enabled"
		if !s.Enabled {
			status = "disabled"
		}
		fmt.Fprintf(&b, "ID:          %s\n", s.ID)
		if s.Description != "" {
			fmt.Fprintf(&b, "Description: %s\n", s.Description)
		}
		fmt.Fprintf(&b, "Cron:        %s\n", s.CronExpr)
		fmt.Fprintf(&b, "Target:      %s/%s\n", s.ChannelName, s.ChannelID)
		fmt.Fprintf(&b, "Status:      %s\n", status)
		fmt.Fprintf(&b, "Created:     %s\n", s.CreatedAt.Format(time.RFC3339))
		if !s.LastFiredAt.IsZero() {
			fmt.Fprintf(&b, "Last fired:  %s\n", s.LastFiredAt.Format(time.RFC3339))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// --- schedule_remove ---

type ScheduleRemoveTool struct {
	store *scheduler.Store
	sched *scheduler.Scheduler
}

func NewScheduleRemoveTool(store *scheduler.Store, sched *scheduler.Scheduler) *ScheduleRemoveTool {
	return &ScheduleRemoveTool{store: store, sched: sched}
}

func (t *ScheduleRemoveTool) Name() string        { return "schedule_remove" }
func (t *ScheduleRemoveTool) Description() string { return "Remove a scheduled task" }
func (t *ScheduleRemoveTool) FullDescription() string {
	return "Remove a scheduled task by ID. Use schedule_list to find IDs."
}

var scheduleRemoveParams = json.RawMessage(`{
	"type": "object",
	"properties": {
		"id": {
			"type": "string",
			"description": "The schedule ID to remove"
		}
	},
	"required": ["id"]
}`)

func (t *ScheduleRemoveTool) Parameters() json.RawMessage { return scheduleRemoveParams }

func (t *ScheduleRemoveTool) Execute(_ context.Context, args string) (string, error) {
	var params struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "Error: invalid arguments: " + err.Error(), nil
	}
	if params.ID == "" {
		return "Error: 'id' is required", nil
	}

	if err := t.store.Remove(params.ID); err != nil {
		return "Error: " + err.Error(), nil
	}

	// Remove from scheduler's cron cache.
	if t.sched != nil {
		t.sched.InvalidateCache(params.ID)
	}

	return fmt.Sprintf("Schedule %s removed.", params.ID), nil
}
