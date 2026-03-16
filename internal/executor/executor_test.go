package executor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/joechenrh/golem/internal/stringutil"
)

func TestLocalExecutor_Echo(t *testing.T) {
	e := NewLocal(t.TempDir())
	r, err := e.Execute(context.Background(), "echo hello", 10*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Stdout != "hello\n" {
		t.Errorf("stdout = %q, want %q", r.Stdout, "hello\n")
	}
	if r.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", r.ExitCode)
	}
	if r.TimedOut {
		t.Error("should not have timed out")
	}
}

func TestLocalExecutor_Stderr(t *testing.T) {
	e := NewLocal(t.TempDir())
	r, err := e.Execute(context.Background(), "echo err >&2", 10*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Stderr != "err\n" {
		t.Errorf("stderr = %q, want %q", r.Stderr, "err\n")
	}
}

func TestLocalExecutor_NonZeroExit(t *testing.T) {
	e := NewLocal(t.TempDir())
	r, err := e.Execute(context.Background(), "exit 42", 10*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.ExitCode != 42 {
		t.Errorf("exit code = %d, want 42", r.ExitCode)
	}
}

func TestLocalExecutor_Timeout(t *testing.T) {
	e := NewLocal(t.TempDir())
	r, err := e.Execute(context.Background(), "sleep 100", 100*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !r.TimedOut {
		t.Error("expected timeout")
	}
}

func TestLocalExecutor_Name(t *testing.T) {
	e := NewLocal(".")
	if e.Name() != "local" {
		t.Errorf("name = %q, want %q", e.Name(), "local")
	}
}

func TestNoopExecutor(t *testing.T) {
	e := NewNoop()
	r, err := e.Execute(context.Background(), "echo hello", 10*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", r.ExitCode)
	}
	if !strings.Contains(r.Stdout, "disabled") {
		t.Errorf("stdout = %q, should contain 'disabled'", r.Stdout)
	}
	if e.Name() != "noop" {
		t.Errorf("name = %q, want %q", e.Name(), "noop")
	}
}

func TestFormatResult(t *testing.T) {
	tests := []struct {
		name   string
		result *Result
		want   string
	}{
		{
			name: "success",
			result: &Result{
				Command: "echo hi",
				Stdout:  "hi\n",
			},
			want: "$ echo hi\nhi\n",
		},
		{
			name: "with stderr and exit code",
			result: &Result{
				Command:  "bad",
				Stderr:   "fail\n",
				ExitCode: 1,
			},
			want: "$ bad\n[stderr]\nfail\n[exit code: 1]\n",
		},
		{
			name: "timed out",
			result: &Result{
				Command:  "sleep 100",
				TimedOut: true,
				ExitCode: -1,
			},
			want: "$ sleep 100\n[timed out]\n[exit code: -1]\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatResult(tt.result)
			if got != tt.want {
				t.Errorf("FormatResult() =\n%q\nwant:\n%q", got, tt.want)
			}
		})
	}
}

func TestRtkRewrite(t *testing.T) {
	tests := []struct {
		name    string
		rtkPath string
		command string
		want    string
	}{
		{
			name:    "no rtk installed",
			rtkPath: "",
			command: "git status",
			want:    "git status",
		},
		{
			name:    "git status",
			rtkPath: "/usr/bin/rtk",
			command: "git status",
			want:    "/usr/bin/rtk git status",
		},
		{
			name:    "cd then git",
			rtkPath: "/usr/bin/rtk",
			command: "cd mydir && git diff",
			want:    "/usr/bin/rtk cd mydir && git diff",
		},
		{
			name:    "go test",
			rtkPath: "/usr/bin/rtk",
			command: "go test ./...",
			want:    "/usr/bin/rtk go test ./...",
		},
		{
			name:    "gh pr list",
			rtkPath: "/usr/bin/rtk",
			command: "gh pr list",
			want:    "/usr/bin/rtk gh pr list",
		},
		{
			name:    "unsupported command",
			rtkPath: "/usr/bin/rtk",
			command: "cat file.txt",
			want:    "cat file.txt",
		},
		{
			name:    "cd then multi-chain skipped",
			rtkPath: "/usr/bin/rtk",
			command: "cd mydir && git status && mysql -e 'SELECT 1'",
			want:    "cd mydir && git status && mysql -e 'SELECT 1'",
		},
		{
			name:    "multi-chain without cd skipped",
			rtkPath: "/usr/bin/rtk",
			command: "git status && git log --oneline",
			want:    "git status && git log --oneline",
		},
		{
			name:    "bare ls",
			rtkPath: "/usr/bin/rtk",
			command: "ls",
			want:    "/usr/bin/rtk ls",
		},
		{
			name:    "rg search",
			rtkPath: "/usr/bin/rtk",
			command: "rg pattern src/",
			want:    "/usr/bin/rtk rg pattern src/",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &LocalExecutor{WorkDir: ".", rtkPath: tt.rtkPath}
			got := e.rtkRewrite(tt.command)
			if got != tt.want {
				t.Errorf("rtkRewrite(%q) = %q, want %q", tt.command, got, tt.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	short := "hello"
	if got := stringutil.TruncateWithNote(short, 100); got != short {
		t.Errorf("stringutil.TruncateWithNote(%q, 100) = %q, want %q", short, got, short)
	}

	long := strings.Repeat("x", 100)
	got := stringutil.TruncateWithNote(long, 50)
	if !strings.HasSuffix(got, "... [truncated]") {
		t.Errorf("truncated output should end with '... [truncated]', got %q", got)
	}
	if len(got) != 50+len("\n... [truncated]") {
		t.Errorf("truncated length = %d, want %d", len(got), 50+len("\n... [truncated]"))
	}
}
