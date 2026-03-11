package builtin

import (
	"context"
	"encoding/json"

	"github.com/joechenrh/golem/internal/hooks"
	"github.com/joechenrh/golem/internal/tools"
)

// ReportProgressTool allows the LLM to send milestone progress updates
// to the user via the hook bus. Only registered on remote channels.
type ReportProgressTool struct {
	hooks *hooks.Bus
}

// NewReportProgressTool creates a progress reporting tool.
func NewReportProgressTool(hookBus *hooks.Bus) *ReportProgressTool {
	return &ReportProgressTool{hooks: hookBus}
}

func (t *ReportProgressTool) Name() string        { return "report_progress" }
func (t *ReportProgressTool) Description() string { return "Report a milestone progress update" }
func (t *ReportProgressTool) FullDescription() string {
	return "Report a milestone progress update to keep the user informed " +
		"about your progress on multi-step tasks. Call this at natural phase " +
		"transitions (e.g., after analysis, before implementation, after completing " +
		"a major subtask). Do NOT call on every tool use — only at meaningful milestones."
}

var reportProgressParams = json.RawMessage(`{
	"type": "object",
	"properties": {
		"summary": {
			"type": "string",
			"description": "A 1-2 sentence milestone update describing what was completed or what is starting next."
		}
	},
	"required": ["summary"]
}`)

func (t *ReportProgressTool) Parameters() json.RawMessage { return reportProgressParams }

func (t *ReportProgressTool) Execute(
	ctx context.Context, args string,
) (string, error) {
	var params struct {
		Summary string `json:"summary"`
	}
	if errMsg := tools.ParseArgs(args, &params); errMsg != "" {
		return errMsg, nil
	}
	if params.Summary == "" {
		return "Error: 'summary' is required", nil
	}

	if err := t.hooks.Emit(ctx, hooks.Event{
		Type:    hooks.EventPhaseUpdate,
		Payload: map[string]any{"summary": params.Summary},
	}); err != nil {
		return "Error: " + err.Error(), nil
	}

	return "Progress reported.", nil
}
