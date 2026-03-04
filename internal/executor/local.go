package executor

import (
	"bytes"
	"context"
	"os/exec"
	"syscall"
	"time"
)

const maxOutputBytes = 50 * 1024 // 50KB

// LocalExecutor runs commands via /bin/sh -c in a working directory.
type LocalExecutor struct {
	WorkDir string
}

// NewLocal creates a LocalExecutor rooted at the given directory.
func NewLocal(workDir string) *LocalExecutor {
	return &LocalExecutor{WorkDir: workDir}
}

func (e *LocalExecutor) Name() string { return "local" }

func (e *LocalExecutor) Execute(ctx context.Context, command string, timeout time.Duration) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Dir = e.WorkDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	result := &Result{
		Stdout:  truncate(stdout.String(), maxOutputBytes),
		Stderr:  truncate(stderr.String(), maxOutputBytes),
		Command: command,
	}

	if ctx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
		result.ExitCode = -1
		return result, nil
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				result.ExitCode = status.ExitStatus()
			} else {
				result.ExitCode = 1
			}
			return result, nil
		}
		return nil, err
	}

	return result, nil
}

func truncate(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes] + "\n... [truncated]"
}
