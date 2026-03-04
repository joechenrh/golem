package executor

import (
	"context"
	"time"
)

// NoopExecutor returns a disabled message for every command.
// Used for read-only deployments or testing.
type NoopExecutor struct{}

func NewNoop() *NoopExecutor { return &NoopExecutor{} }

func (n *NoopExecutor) Name() string { return "noop" }

func (n *NoopExecutor) Execute(_ context.Context, cmd string, _ time.Duration) (*Result, error) {
	return &Result{
		Stdout:   "Command execution is disabled in this mode.",
		ExitCode: 1,
		Command:  cmd,
	}, nil
}
