package executor

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Result holds the output of a command execution.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
	TimedOut bool
	Command  string
}

// Executor runs commands in some environment.
type Executor interface {
	Execute(ctx context.Context, command string, timeout time.Duration) (*Result, error)
	Name() string // "local", "docker", "noop"
}

// FormatResult returns a human-readable string for LLM consumption.
func FormatResult(r *Result) string {
	var b strings.Builder

	fmt.Fprintf(&b, "$ %s\n", r.Command)

	if r.TimedOut {
		b.WriteString("[timed out]\n")
	}

	if r.Stdout != "" {
		b.WriteString(r.Stdout)
		if !strings.HasSuffix(r.Stdout, "\n") {
			b.WriteByte('\n')
		}
	}

	if r.Stderr != "" {
		fmt.Fprintf(&b, "[stderr]\n%s", r.Stderr)
		if !strings.HasSuffix(r.Stderr, "\n") {
			b.WriteByte('\n')
		}
	}

	if r.ExitCode != 0 {
		fmt.Fprintf(&b, "[exit code: %d]\n", r.ExitCode)
	}

	return b.String()
}
