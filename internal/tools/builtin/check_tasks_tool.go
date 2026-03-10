package builtin

import (
	"context"
	"encoding/json"

	"github.com/joechenrh/golem/internal/tools"
)

// CheckTasksTool reports the status of background tasks.
type CheckTasksTool struct{}

func NewCheckTasksTool() *CheckTasksTool { return &CheckTasksTool{} }

func (t *CheckTasksTool) Name() string { return "check_tasks" }
func (t *CheckTasksTool) Description() string {
	return "Check status and results of background tasks"
}
func (t *CheckTasksTool) FullDescription() string {
	return "Check the status and results of background tasks spawned by spawn_agent. " +
		"Returns a formatted list showing each task's ID, status (running/completed/failed), " +
		"description, and timing information. Use this to monitor progress and decide " +
		"whether to wait, retry failures, or deliver final results to the user."
}

var checkTasksParams = json.RawMessage(`{
	"type": "object",
	"properties": {}
}`)

func (t *CheckTasksTool) Parameters() json.RawMessage { return checkTasksParams }

func (t *CheckTasksTool) Execute(ctx context.Context, _ string) (string, error) {
	tracker := tools.GetTaskTracker(ctx)
	if tracker == nil {
		return "No task tracker available.", nil
	}
	return tracker.Summary(), nil
}
