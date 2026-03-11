package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/joechenrh/golem/internal/hooks"
)

func TestFormatProgress(t *testing.T) {
	tests := []struct {
		name     string
		snap     StatusSnapshot
		contains []string
		absent   []string
	}{
		{
			name: "basic iteration display",
			snap: StatusSnapshot{
				State: SessionState{Iteration: 3, MaxIter: 15},
			},
			contains: []string{"3/15"},
		},
		{
			name: "with phase",
			snap: StatusSnapshot{
				State: SessionState{
					Iteration: 5, MaxIter: 15,
					Phase: "implementing error handling",
				},
			},
			contains: []string{`"implementing error handling"`},
		},
		{
			name: "with active tool",
			snap: StatusSnapshot{
				State: SessionState{
					Iteration: 7, MaxIter: 15,
					ActiveTool:  "shell_exec",
					ToolStarted: time.Now().Add(-3 * time.Second),
				},
			},
			contains: []string{"shell_exec", "running", "elapsed"},
		},
		{
			name: "with recent tool events",
			snap: StatusSnapshot{
				State: SessionState{Iteration: 5, MaxIter: 15},
				RecentEvents: []accEvent{
					{Type: hooks.EventAfterToolExec, Payload: map[string]any{
						"tool_name": "read_file", "duration_ms": int64(50),
					}},
					{Type: hooks.EventAfterToolExec, Payload: map[string]any{
						"tool_name": "edit_file", "duration_ms": int64(80),
						"error": "permission denied",
					}},
				},
			},
			contains: []string{"read_file", "edit_file", "error"},
		},
		{
			name: "with running tasks",
			snap: StatusSnapshot{
				State: SessionState{Iteration: 2, MaxIter: 15, RunningTasks: 3},
			},
			contains: []string{"3 running"},
		},
		{
			name: "no recent events section when empty",
			snap: StatusSnapshot{
				State: SessionState{Iteration: 1, MaxIter: 15},
			},
			absent: []string{"Recent activity"},
		},
		{
			name: "no tasks section when zero",
			snap: StatusSnapshot{
				State: SessionState{Iteration: 1, MaxIter: 15, RunningTasks: 0},
			},
			absent: []string{"Background tasks"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatProgress(tt.snap)
			for _, s := range tt.contains {
				if !strings.Contains(result, s) {
					t.Errorf("expected output to contain %q, got:\n%s", s, result)
				}
			}
			for _, s := range tt.absent {
				if strings.Contains(result, s) {
					t.Errorf("expected output NOT to contain %q, got:\n%s", s, result)
				}
			}
		})
	}
}
