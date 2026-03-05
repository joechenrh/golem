package builtin

import (
	"context"
	"encoding/json"
	"fmt"
)

// SubAgentRunner runs a prompt through a sub-agent and returns the response.
// The implementation should create a fresh agent loop WITHOUT spawn capability
// to prevent recursive spawning.
type SubAgentRunner func(ctx context.Context, prompt string) (string, error)

// SpawnAgentTool delegates a task to an independent sub-agent.
type SpawnAgentTool struct {
	runner SubAgentRunner
}

// NewSpawnAgentTool creates a spawn tool using the provided runner function.
// The runner is responsible for constructing a sub-agent without spawn_agent
// in its tool registry.
func NewSpawnAgentTool(runner SubAgentRunner) *SpawnAgentTool {
	return &SpawnAgentTool{runner: runner}
}

func (t *SpawnAgentTool) Name() string        { return "spawn_agent" }
func (t *SpawnAgentTool) Description() string { return "Delegate a task to an independent sub-agent" }
func (t *SpawnAgentTool) FullDescription() string {
	return "Spawn an independent sub-agent to handle a delegated task. " +
		"The sub-agent has its own conversation context and access to standard tools " +
		"(shell, file I/O, web) but cannot spawn further agents. " +
		"Use this for self-contained subtasks that benefit from a clean context."
}

var spawnAgentParams = json.RawMessage(`{
	"type": "object",
	"properties": {
		"prompt": {
			"type": "string",
			"description": "The task description for the sub-agent. Be specific about what you need it to do and what output you expect."
		}
	},
	"required": ["prompt"]
}`)

func (t *SpawnAgentTool) Parameters() json.RawMessage { return spawnAgentParams }

func (t *SpawnAgentTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if params.Prompt == "" {
		return "Error: 'prompt' is required", nil
	}

	result, err := t.runner(ctx, params.Prompt)
	if err != nil {
		return "Sub-agent error: " + err.Error(), nil
	}
	return result, nil
}
