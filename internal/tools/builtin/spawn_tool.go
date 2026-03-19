package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/joechenrh/golem/internal/tools"
)

// SubAgentSession holds a created sub-agent session ready to run.
type SubAgentSession struct {
	Tracker tools.BackgroundTaskTracker
	Runner  func(ctx context.Context, prompt string) (string, error)
}

// SessionCreator creates a sub-agent session. Called from Execute()
// before launching the background task.
type SessionCreator func(ctx context.Context) (*SubAgentSession, error)

// SpawnAgentTool delegates a task to an independent sub-agent.
type SpawnAgentTool struct {
	creator SessionCreator
}

// NewSpawnAgentTool creates a spawn tool using the provided session creator.
// The creator is responsible for constructing a sub-agent session, returning
// a tracker for tree display and a runner for executing the prompt.
func NewSpawnAgentTool(creator SessionCreator) *SpawnAgentTool {
	return &SpawnAgentTool{creator: creator}
}

func (t *SpawnAgentTool) Name() string        { return "spawn_agent" }
func (t *SpawnAgentTool) Description() string { return "Delegate a task to an independent sub-agent" }
func (t *SpawnAgentTool) FullDescription() string {
	return "Spawn a background sub-agent to handle a task asynchronously. " +
		"Returns immediately with a task ID — the sub-agent works in the background.\n\n" +
		"PREFER spawning sub-agents over doing complex work in the main session. " +
		"For any task involving code changes, debugging, file analysis, or multi-step " +
		"investigations, delegate to a sub-agent so you can report progress to the user " +
		"and coordinate multiple tasks in parallel.\n\n" +
		"The sub-agent has its own conversation context and access to standard tools " +
		"(shell, file I/O, web). Sub-agents can spawn further sub-agents up to the configured depth limit.\n\n" +
		"You can call this tool multiple times in a single response to run several " +
		"sub-agents in parallel. Results are delivered automatically when each finishes."
}

var spawnAgentParams = json.RawMessage(`{
	"type": "object",
	"properties": {
		"prompt": {
			"type": "string",
			"description": "The task description for the sub-agent. Be specific about what you need it to do and what output you expect."
		},
		"context": {
			"type": "string",
			"description": "Key context, files, or partial results to pass to the sub-agent. This is prepended to the prompt so the sub-agent has the information it needs without re-discovering it."
		},
		"description": {
			"type": "string",
			"description": "Short human-readable task name for status display, e.g. 'fix #67041'. If omitted, a truncated prompt is used."
		}
	},
	"required": ["prompt"]
}`)

func (t *SpawnAgentTool) Parameters() json.RawMessage { return spawnAgentParams }

func (t *SpawnAgentTool) Execute(
	ctx context.Context, args string,
) (string, error) {
	var params struct {
		Prompt      string `json:"prompt"`
		Context     string `json:"context"`
		Description string `json:"description"`
	}
	if errMsg := tools.ParseArgs(args, &params); errMsg != "" {
		return errMsg, nil
	}
	if params.Prompt == "" {
		return "Error: 'prompt' is required", nil
	}

	prompt := params.Prompt
	if params.Context != "" {
		prompt = "[Context from parent agent]\n" + params.Context + "\n\n[Task]\n" + params.Prompt
	}

	tracker := tools.GetTaskTracker(ctx)
	if tracker == nil {
		// Sync fallback for sub-sessions (which have no tracker).
		sub, err := t.creator(ctx)
		if err != nil {
			return "Sub-agent error: " + err.Error(), nil
		}
		result, err := sub.Runner(ctx, prompt)
		if err != nil {
			return "Sub-agent error: " + err.Error(), nil
		}
		return result, nil
	}

	// Async: Launch manages context, errgroup, and lifecycle.
	desc := params.Description
	if desc == "" {
		desc = truncateDesc(params.Prompt, 100)
	}
	capturedPrompt := prompt
	taskID := tracker.Launch(desc, func(taskCtx context.Context, id int) {
		sub, err := t.creator(taskCtx)
		if err != nil {
			tracker.Fail(id, err.Error())
			return
		}
		// Link child tracker for tree display before running.
		if sub.Tracker != nil {
			tracker.SetChildTracker(id, sub.Tracker)
		}
		result, err := sub.Runner(taskCtx, capturedPrompt)
		if err != nil {
			tracker.Fail(id, err.Error())
		} else {
			tracker.Complete(id, result)
		}
	})

	return fmt.Sprintf("Task #%d started: %s\nResults will be delivered automatically when the sub-agent finishes.", taskID, desc), nil
}

// truncateDesc truncates s to maxLen characters, appending "…" if truncated.
func truncateDesc(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}
