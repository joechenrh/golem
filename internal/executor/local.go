package executor

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/joechenrh/golem/internal/stringutil"
)

const maxOutputBytes = 50 * 1024 // 50KB

// rtkCommands lists command prefixes that RTK can compress.
// See https://github.com/rtk-ai/rtk for the full list.
var rtkCommands = []string{
	"git ", "gh ",
	"cargo ", "go test", "pytest", "vitest", "playwright",
	"eslint", "biome", "tsc", "ruff", "golangci-lint", "clippy",
	"docker ", "kubectl ",
	"npm ", "pnpm ", "pip ", "prisma ",
	"ls ", "ls\n", "find ", "grep ", "rg ", "diff ",
}

// LocalExecutor runs commands via /bin/sh -c in a working directory.
type LocalExecutor struct {
	WorkDir    string
	rtkPath    string // empty if RTK is not installed
	DisableRTK bool   // when true, skip rtk rewriting even if installed
}

// NewLocal creates a LocalExecutor rooted at the given directory.
// It auto-detects RTK on PATH for transparent output compression.
func NewLocal(workDir string) *LocalExecutor {
	e := &LocalExecutor{WorkDir: workDir}
	if path, err := exec.LookPath("rtk"); err == nil {
		e.rtkPath = path
	}
	return e
}

func (e *LocalExecutor) Name() string { return "local" }

func (e *LocalExecutor) Execute(
	ctx context.Context, command string,
	timeout time.Duration,
) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	shellCmd := e.rtkRewrite(command)
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", shellCmd)
	cmd.Dir = e.WorkDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	result := &Result{
		Stdout:  stringutil.TruncateHeadTail(stdout.String(), maxOutputBytes),
		Stderr:  stringutil.TruncateHeadTail(stderr.String(), maxOutputBytes),
		Command: command,
	}

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.TimedOut = true
		result.ExitCode = -1
		return result, nil
	}

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
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

// rtkRewrite prefixes a command with "rtk" when RTK is installed and the
// command is one that RTK knows how to compress. The original command is
// returned unchanged when RTK is not available or the command is unsupported.
//
// Only simple commands are rewritten: either a bare command ("git status")
// or a single "cd <dir> && <cmd>" pattern. Multi-command chains like
// "git status && mysql ..." are left alone because rtk cannot handle them.
func (e *LocalExecutor) rtkRewrite(command string) string {
	if e.rtkPath == "" || e.DisableRTK {
		return command
	}
	// Strip leading whitespace to find the actual command.
	bare := strings.TrimLeft(command, " \t")
	// Allow a single "cd <dir> && <cmd>" prefix — strip it to reach the
	// real command. Any other use of "&&" means a multi-command chain
	// that rtk cannot handle, so skip rewriting entirely.
	if idx := strings.Index(bare, "&& "); idx >= 0 {
		before := strings.TrimSpace(bare[:idx])
		if !strings.HasPrefix(before, "cd ") && before != "cd" {
			// First segment is not cd — this is a multi-command chain.
			return command
		}
		rest := strings.TrimLeft(bare[idx+3:], " \t")
		if strings.Contains(rest, "&& ") {
			// More than one command after cd — still a chain.
			return command
		}
		bare = rest
	}
	for _, prefix := range rtkCommands {
		if strings.HasPrefix(bare, prefix) || bare == strings.TrimRight(prefix, " \n") {
			return e.rtkPath + " " + command
		}
	}
	return command
}
