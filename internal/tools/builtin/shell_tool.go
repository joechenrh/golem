package builtin

import (
	"context"
	"encoding/json"
	"time"

	"github.com/joechenrh/golem/internal/executor"
	"github.com/joechenrh/golem/internal/llm"
)

var shellParams = json.RawMessage(`{
	"type": "object",
	"properties": {
		"command": {"type": "string", "description": "Shell command to execute"},
		"timeout": {"type": "integer", "description": "Timeout in seconds (optional, default 30)"}
	},
	"required": ["command"]
}`)

// ShellTool executes shell commands via an Executor.
type ShellTool struct {
	exec           executor.Executor
	defaultTimeout time.Duration
}

// NewShellTool creates a shell execution tool backed by the given executor.
func NewShellTool(
	exec executor.Executor, defaultTimeout time.Duration,
) *ShellTool {
	return &ShellTool{exec: exec, defaultTimeout: defaultTimeout}
}

func (t *ShellTool) Name() string        { return "shell_exec" }
func (t *ShellTool) Description() string { return "Execute a shell command" }
func (t *ShellTool) FullDescription() string {
	return "Execute a shell command in the workspace directory. Returns stdout, stderr, and exit code."
}
func (t *ShellTool) Parameters() json.RawMessage { return shellParams }

func (t *ShellTool) Execute(
	ctx context.Context, args string,
) (string, error) {
	var params struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	if err := json.Unmarshal([]byte(llm.NormalizeArgs(args)), &params); err != nil {
		return "Error: invalid arguments: " + err.Error(), nil
	}
	if params.Command == "" {
		return "Error: 'command' is required", nil
	}

	timeout := t.defaultTimeout
	if params.Timeout > 0 {
		timeout = time.Duration(params.Timeout) * time.Second
	}

	result, err := t.exec.Execute(ctx, params.Command, timeout)
	if err != nil {
		return "Error executing command: " + err.Error(), nil
	}

	return executor.FormatResult(result), nil
}
