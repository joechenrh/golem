package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/joechenrh/golem/internal/hooks"
	"github.com/joechenrh/golem/internal/stringutil"
)

// TaskStatus represents the state of a background task.
type TaskStatus int

const (
	TaskRunning TaskStatus = iota
	TaskCompleted
	TaskFailed
)

func (s TaskStatus) String() string {
	switch s {
	case TaskRunning:
		return "running"
	case TaskCompleted:
		return "completed"
	case TaskFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// BackgroundTask holds the state of a single background task.
type BackgroundTask struct {
	ID          int
	Description string
	Status      TaskStatus
	StartedAt   time.Time
	CompletedAt time.Time
	Result      string
	Error       string
	cancel      context.CancelFunc
	injected    bool // true once DrainCompleted has returned this task
}

// TaskTracker manages background tasks launched by spawn_agent.
// It uses errgroup for goroutine lifecycle management.
type TaskTracker struct {
	mu    sync.Mutex
	tasks map[int]*BackgroundTask
	seq   int
	g     errgroup.Group
	done  chan struct{} // closed/re-created when any task completes
	hooks *hooks.Bus    // optional, nil for sub-agent trackers
}

// NewTaskTracker creates a tracker that allows up to maxConcurrent
// background tasks to run simultaneously.
func NewTaskTracker(maxConcurrent int) *TaskTracker {
	tt := &TaskTracker{
		tasks: make(map[int]*BackgroundTask),
		done:  make(chan struct{}),
	}
	tt.g.SetLimit(maxConcurrent)
	return tt
}

// SetHooks attaches a hook bus for emitting task lifecycle events.
func (tt *TaskTracker) SetHooks(bus *hooks.Bus) {
	tt.hooks = bus
}

// Add registers a new running task and returns its ID.
func (tt *TaskTracker) Add(desc string, cancel context.CancelFunc) int {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	tt.seq++
	tt.tasks[tt.seq] = &BackgroundTask{
		ID:          tt.seq,
		Description: desc,
		Status:      TaskRunning,
		StartedAt:   time.Now(),
		cancel:      cancel,
	}
	return tt.seq
}

// Launch creates a context, registers the task, and starts a goroutine
// via errgroup. The task ID is passed to fn to avoid closure capture races.
// Uses context.WithoutCancel so the subagent survives the parent's
// processToolCalls errgroup cancellation.
func (tt *TaskTracker) Launch(desc string, fn func(ctx context.Context, id int)) int {
	// Use WithoutCancel so the background task is not cancelled when
	// the parent tool-call errgroup finishes.
	ctx, cancel := context.WithCancel(context.WithoutCancel(context.Background()))
	id := tt.Add(desc, cancel)
	if tt.hooks != nil {
		tt.hooks.Emit(context.Background(), hooks.Event{
			Type: hooks.EventTaskLaunched,
			Payload: map[string]any{
				"task_id":     id,
				"description": desc,
			},
		})
	}
	tt.g.Go(func() error {
		fn(ctx, id)
		return nil
	})
	return id
}

// Complete marks a task as successfully completed.
func (tt *TaskTracker) Complete(id int, result string) {
	tt.mu.Lock()
	var desc string
	if t, ok := tt.tasks[id]; ok {
		t.Status = TaskCompleted
		t.CompletedAt = time.Now()
		t.Result = result
		desc = t.Description
	}
	tt.signalDone()
	tt.mu.Unlock()

	if tt.hooks != nil && desc != "" {
		tt.hooks.Emit(context.Background(), hooks.Event{
			Type: hooks.EventTaskCompleted,
			Payload: map[string]any{
				"task_id":     id,
				"description": desc,
				"result":      stringutil.Truncate(result, 200),
			},
		})
	}
}

// Fail marks a task as failed.
func (tt *TaskTracker) Fail(id int, errMsg string) {
	tt.mu.Lock()
	var desc string
	if t, ok := tt.tasks[id]; ok {
		t.Status = TaskFailed
		t.CompletedAt = time.Now()
		t.Error = errMsg
		desc = t.Description
	}
	tt.signalDone()
	tt.mu.Unlock()

	if tt.hooks != nil && desc != "" {
		tt.hooks.Emit(context.Background(), hooks.Event{
			Type: hooks.EventTaskCompleted,
			Payload: map[string]any{
				"task_id":     id,
				"description": desc,
				"error":       errMsg,
			},
		})
	}
}

// signalDone wakes any goroutine blocked in WaitForAny.
// Must be called with tt.mu held.
func (tt *TaskTracker) signalDone() {
	close(tt.done)
	tt.done = make(chan struct{})
}

// HasRunning returns true if any task is still running.
func (tt *TaskTracker) HasRunning() bool {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	for _, t := range tt.tasks {
		if t.Status == TaskRunning {
			return true
		}
	}
	return false
}

// WaitForAny blocks until at least one task completes (or fails),
// or the context is cancelled. Returns immediately if no tasks are running
// or uninjected results already exist.
func (tt *TaskTracker) WaitForAny(ctx context.Context) {
	for {
		tt.mu.Lock()
		hasRunning := false
		hasUninjected := false
		for _, t := range tt.tasks {
			if t.Status == TaskRunning {
				hasRunning = true
			}
			if !t.injected && (t.Status == TaskCompleted || t.Status == TaskFailed) {
				hasUninjected = true
			}
		}
		ch := tt.done
		tt.mu.Unlock()

		// Nothing running or there are already uninjected results — return.
		if !hasRunning || hasUninjected {
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-ch:
			// A task completed — loop to check state.
		}
	}
}

// DrainCompleted returns finished (completed or failed) tasks that have
// not yet been injected, and marks them as injected.
func (tt *TaskTracker) DrainCompleted() []BackgroundTask {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	var out []BackgroundTask
	for _, t := range tt.tasks {
		if t.injected {
			continue
		}
		if t.Status == TaskCompleted || t.Status == TaskFailed {
			t.injected = true
			out = append(out, *t) // copy
		}
	}
	return out
}

// Summary returns a formatted status of all tasks.
func (tt *TaskTracker) Summary() string {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	if len(tt.tasks) == 0 {
		return "No background tasks."
	}
	var b strings.Builder
	b.WriteString("Background tasks:\n")
	for i := range tt.seq {
		id := i + 1
		t, ok := tt.tasks[id]
		if !ok {
			continue
		}
		switch t.Status {
		case TaskRunning:
			fmt.Fprintf(&b, "  #%d [running]   %q — running for %s\n",
				t.ID, t.Description, time.Since(t.StartedAt).Truncate(time.Second))
		case TaskCompleted:
			fmt.Fprintf(&b, "  #%d [completed] %q — finished %s ago (result: %d chars)\n",
				t.ID, t.Description, time.Since(t.CompletedAt).Truncate(time.Second), len(t.Result))
		case TaskFailed:
			fmt.Fprintf(&b, "  #%d [failed]    %q — error: %s\n",
				t.ID, t.Description, t.Error)
		}
	}
	return b.String()
}

// CancelAll cancels all running tasks.
func (tt *TaskTracker) CancelAll() {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	for _, t := range tt.tasks {
		if t.Status == TaskRunning && t.cancel != nil {
			t.cancel()
		}
	}
}

// Close cancels all running tasks and waits for all goroutines to finish.
func (tt *TaskTracker) Close() {
	tt.CancelAll()
	tt.g.Wait()
}
