# Observable Progress Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make long-running agent work observable on remote channels (Lark/Telegram) via milestone progress updates and on-demand `/status` queries.

**Architecture:** Extend the existing `hooks.Bus` with new event types. An `EventAccumulator` hook accumulates events for status snapshots. A `ProgressReporter` hook sends milestone updates to chat. The existing `/status` slash command is extended to show progress.

**Tech Stack:** Go 1.22+, existing `hooks.Bus`, `hooks.Hook` interface, `channel.Channel` interface.

**Spec:** `docs/superpowers/specs/2026-03-11-observable-progress-design.md`

---

## File Structure

| File | Responsibility |
|------|---------------|
| `internal/hooks/hooks.go` | Extended — 6 new `EventType` constants |
| `internal/agent/accumulator.go` | New — `EventAccumulator` (implements `hooks.Hook`), `SessionState`, `StatusSnapshot` |
| `internal/agent/accumulator_test.go` | New — unit tests for accumulator |
| `internal/agent/progress_reporter.go` | New — `ProgressReporter` (implements `hooks.Hook`), throttled chat updates |
| `internal/agent/progress_reporter_test.go` | New — unit tests for reporter |
| `internal/agent/progress_format.go` | New — `FormatProgress()` function + helpers |
| `internal/agent/progress_format_test.go` | New — table-driven format tests |
| `internal/tools/builtin/progress_tool.go` | New — `ReportProgressTool` |
| `internal/tools/builtin/progress_tool_test.go` | New — unit test for tool |
| `internal/agent/session.go` | Modified — add `accumulator` field, extend `StatusInfo()` |
| `internal/agent/react.go` | Modified — emit `turn_start`, `iteration_start`, defer `turn_done` |
| `internal/agent/tasks.go` | Modified — add `hooks *hooks.Bus` field, emit in `Launch`/`Complete`/`Fail` |
| `internal/agent/manager.go` | Modified — wire accumulator + reporter in `createSession()` |
| `internal/app/app.go` | Modified — wire accumulator + reporter + progress tool in `BuildAgent` |

---

## Chunk 1: Event Types + EventAccumulator

### Task 1: Add new EventType constants to hooks.go

**Files:**
- Modify: `internal/hooks/hooks.go:15-22`

- [ ] **Step 1: Add the 6 new event type constants**

In `internal/hooks/hooks.go`, extend the existing `const` block after line 21:

```go
const (
	EventUserMessage    EventType = "user_message"
	EventBeforeLLMCall  EventType = "before_llm_call"
	EventAfterLLMCall   EventType = "after_llm_call"
	EventBeforeToolExec EventType = "before_tool_exec"
	EventAfterToolExec  EventType = "after_tool_exec"
	EventError          EventType = "error"

	// Progress tracking events.
	EventIterationStart EventType = "iteration_start"
	EventPhaseUpdate    EventType = "phase_update"
	EventTaskLaunched   EventType = "task_launched"
	EventTaskCompleted  EventType = "task_completed"
	EventTurnStart      EventType = "turn_start"
	EventTurnDone       EventType = "turn_done"
)
```

- [ ] **Step 2: Verify the build**

Run: `cd /mnt/data/joechenrh/golem && go build ./internal/hooks/...`
Expected: Success, no errors.

- [ ] **Step 3: Commit**

```bash
git add internal/hooks/hooks.go
git commit -m "feat: add progress tracking event types to hook bus"
```

---

### Task 2: Create EventAccumulator with tests (TDD)

**Files:**
- Create: `internal/agent/accumulator.go`
- Create: `internal/agent/accumulator_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/agent/accumulator_test.go`:

```go
package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/joechenrh/golem/internal/hooks"
)

func TestEventAccumulator_Handle(t *testing.T) {
	tests := []struct {
		name   string
		events []hooks.Event
		check  func(t *testing.T, snap StatusSnapshot)
	}{
		{
			name: "turn_start resets state",
			events: []hooks.Event{
				{Type: hooks.EventIterationStart, Payload: map[string]any{"iteration": 3, "max_iter": 15}},
				{Type: hooks.EventTurnStart, Payload: map[string]any{"user_message": "hello"}},
			},
			check: func(t *testing.T, snap StatusSnapshot) {
				if snap.State.Iteration != 0 {
					t.Errorf("expected iteration 0 after reset, got %d", snap.State.Iteration)
				}
				// turn_start itself is in events (reset clears before appending)
				if len(snap.RecentEvents) != 1 {
					t.Errorf("expected 1 event after reset, got %d", len(snap.RecentEvents))
				}
			},
		},
		{
			name: "iteration_start updates state",
			events: []hooks.Event{
				{Type: hooks.EventIterationStart, Payload: map[string]any{"iteration": 5, "max_iter": 15}},
			},
			check: func(t *testing.T, snap StatusSnapshot) {
				if snap.State.Iteration != 5 {
					t.Errorf("expected iteration 5, got %d", snap.State.Iteration)
				}
				if snap.State.MaxIter != 15 {
					t.Errorf("expected max_iter 15, got %d", snap.State.MaxIter)
				}
			},
		},
		{
			name: "tool lifecycle updates active tool",
			events: []hooks.Event{
				{Type: hooks.EventBeforeToolExec, Payload: map[string]any{"tool_name": "shell_exec"}},
			},
			check: func(t *testing.T, snap StatusSnapshot) {
				if snap.State.ActiveTool != "shell_exec" {
					t.Errorf("expected active tool shell_exec, got %q", snap.State.ActiveTool)
				}
			},
		},
		{
			name: "after_tool_exec clears active tool",
			events: []hooks.Event{
				{Type: hooks.EventBeforeToolExec, Payload: map[string]any{"tool_name": "shell_exec"}},
				{Type: hooks.EventAfterToolExec, Payload: map[string]any{"tool_name": "shell_exec", "duration_ms": int64(100)}},
			},
			check: func(t *testing.T, snap StatusSnapshot) {
				if snap.State.ActiveTool != "" {
					t.Errorf("expected no active tool, got %q", snap.State.ActiveTool)
				}
			},
		},
		{
			name: "phase_update sets phase",
			events: []hooks.Event{
				{Type: hooks.EventPhaseUpdate, Payload: map[string]any{"summary": "analyzing codebase"}},
			},
			check: func(t *testing.T, snap StatusSnapshot) {
				if snap.State.Phase != "analyzing codebase" {
					t.Errorf("expected phase %q, got %q", "analyzing codebase", snap.State.Phase)
				}
			},
		},
		{
			name: "task_launched increments running tasks",
			events: []hooks.Event{
				{Type: hooks.EventTaskLaunched, Payload: map[string]any{"task_id": 1, "description": "test"}},
				{Type: hooks.EventTaskLaunched, Payload: map[string]any{"task_id": 2, "description": "test2"}},
			},
			check: func(t *testing.T, snap StatusSnapshot) {
				if snap.State.RunningTasks != 2 {
					t.Errorf("expected 2 running tasks, got %d", snap.State.RunningTasks)
				}
			},
		},
		{
			name: "task_completed decrements running tasks",
			events: []hooks.Event{
				{Type: hooks.EventTaskLaunched, Payload: map[string]any{"task_id": 1, "description": "test"}},
				{Type: hooks.EventTaskCompleted, Payload: map[string]any{"task_id": 1, "result": "done"}},
			},
			check: func(t *testing.T, snap StatusSnapshot) {
				if snap.State.RunningTasks != 0 {
					t.Errorf("expected 0 running tasks, got %d", snap.State.RunningTasks)
				}
			},
		},
		{
			name: "turn_done sets idle",
			events: []hooks.Event{
				{Type: hooks.EventTurnDone, Payload: map[string]any{"tokens_used": 100}},
			},
			check: func(t *testing.T, snap StatusSnapshot) {
				if snap.State.IdleSince == nil {
					t.Error("expected IdleSince to be set")
				}
				if snap.State.ActiveTool != "" {
					t.Errorf("expected no active tool after turn_done, got %q", snap.State.ActiveTool)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acc := NewEventAccumulator(50)
			for _, evt := range tt.events {
				acc.Handle(context.Background(), evt)
			}
			snap := acc.Snapshot()
			tt.check(t, snap)
		})
	}
}

func TestEventAccumulator_RollingWindow(t *testing.T) {
	acc := NewEventAccumulator(3) // tiny window
	for i := range 5 {
		acc.Handle(context.Background(), hooks.Event{
			Type:    hooks.EventIterationStart,
			Payload: map[string]any{"iteration": i, "max_iter": 15},
		})
	}
	snap := acc.Snapshot()
	// Window size 3, so only last 3 events kept. Snapshot returns min(5, len) = 3.
	if len(snap.RecentEvents) != 3 {
		t.Errorf("expected 3 recent events, got %d", len(snap.RecentEvents))
	}
}

func TestEventAccumulator_SnapshotLastN(t *testing.T) {
	acc := NewEventAccumulator(50)
	for i := range 10 {
		acc.Handle(context.Background(), hooks.Event{
			Type:    hooks.EventIterationStart,
			Payload: map[string]any{"iteration": i, "max_iter": 15},
		})
	}
	snap := acc.Snapshot()
	// Snapshot returns last 5 regardless of window size.
	if len(snap.RecentEvents) != 5 {
		t.Errorf("expected 5 recent events, got %d", len(snap.RecentEvents))
	}
}

func TestEventAccumulator_ConcurrentAccess(t *testing.T) {
	acc := NewEventAccumulator(50)
	var wg sync.WaitGroup

	// Writer goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 100 {
			acc.Handle(context.Background(), hooks.Event{
				Type:    hooks.EventIterationStart,
				Payload: map[string]any{"iteration": i, "max_iter": 100},
			})
		}
	}()

	// Reader goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 100 {
			_ = acc.Snapshot()
		}
	}()

	wg.Wait()
	// If we get here without a race detector panic, concurrent access is safe.
}

func TestEventAccumulator_TaskCompletedNeverNegative(t *testing.T) {
	acc := NewEventAccumulator(50)
	// Complete without a prior launch — should not go negative.
	acc.Handle(context.Background(), hooks.Event{
		Type:    hooks.EventTaskCompleted,
		Payload: map[string]any{"task_id": 1},
	})
	snap := acc.Snapshot()
	if snap.State.RunningTasks != 0 {
		t.Errorf("expected 0 running tasks, got %d", snap.State.RunningTasks)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /mnt/data/joechenrh/golem && go test ./internal/agent/ -run TestEventAccumulator -v`
Expected: FAIL — `NewEventAccumulator` undefined.

- [ ] **Step 3: Implement EventAccumulator**

Create `internal/agent/accumulator.go`:

```go
package agent

import (
	"context"
	"sync"
	"time"

	"github.com/joechenrh/golem/internal/hooks"
)

// EventAccumulator implements hooks.Hook and accumulates lifecycle events
// for progress tracking. Register it on a hooks.Bus to capture tool
// execution, iteration, and phase events. Call Snapshot() from any
// goroutine to read the current state without disrupting the ReAct loop.
type EventAccumulator struct {
	mu        sync.RWMutex
	events    []accEvent
	maxEvents int
	current   SessionState
}

// accEvent is an internal event record stored in the rolling window.
type accEvent struct {
	Type      hooks.EventType
	Timestamp time.Time
	Payload   map[string]any
}

// SessionState tracks the live progress of a session.
type SessionState struct {
	Iteration    int
	MaxIter      int
	ActiveTool   string
	ToolStarted  time.Time
	Phase        string
	RunningTasks int
	IdleSince    *time.Time
}

// StatusSnapshot is a point-in-time copy of the accumulator state,
// safe to read after Snapshot() returns.
type StatusSnapshot struct {
	State        SessionState
	RecentEvents []accEvent
}

// NewEventAccumulator creates an accumulator with the given rolling window size.
func NewEventAccumulator(maxEvents int) *EventAccumulator {
	return &EventAccumulator{
		maxEvents: maxEvents,
	}
}

// Name implements hooks.Hook.
func (a *EventAccumulator) Name() string { return "accumulator" }

// Handle implements hooks.Hook. Called by hooks.Bus.Emit for every event.
func (a *EventAccumulator) Handle(_ context.Context, event hooks.Event) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Reset on turn start before recording the event.
	if event.Type == hooks.EventTurnStart {
		a.events = a.events[:0]
		a.current = SessionState{}
	}

	entry := accEvent{
		Type:      event.Type,
		Timestamp: time.Now(),
		Payload:   event.Payload,
	}

	if len(a.events) >= a.maxEvents {
		a.events = a.events[1:]
	}
	a.events = append(a.events, entry)

	switch event.Type {
	case hooks.EventBeforeToolExec:
		a.current.ActiveTool = payloadStr(event.Payload, "tool_name")
		a.current.ToolStarted = time.Now()
		a.current.IdleSince = nil
	case hooks.EventAfterToolExec:
		a.current.ActiveTool = ""
	case hooks.EventIterationStart:
		a.current.Iteration = payloadInt(event.Payload, "iteration")
		a.current.MaxIter = payloadInt(event.Payload, "max_iter")
		a.current.IdleSince = nil
	case hooks.EventPhaseUpdate:
		a.current.Phase = payloadStr(event.Payload, "summary")
	case hooks.EventTaskLaunched:
		a.current.RunningTasks++
	case hooks.EventTaskCompleted:
		a.current.RunningTasks = max(0, a.current.RunningTasks-1)
	case hooks.EventTurnStart:
		a.current.IdleSince = nil
	case hooks.EventTurnDone:
		now := time.Now()
		a.current.IdleSince = &now
		a.current.ActiveTool = ""
	}

	return nil
}

// Snapshot returns a point-in-time copy of the current state and last 5 events.
// Safe to call concurrently from the StatusHandler goroutine.
func (a *EventAccumulator) Snapshot() StatusSnapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()

	n := min(5, len(a.events))
	recent := make([]accEvent, n)
	copy(recent, a.events[len(a.events)-n:])

	return StatusSnapshot{
		State:        a.current,
		RecentEvents: recent,
	}
}

// payloadStr extracts a string value from the event payload.
func payloadStr(p map[string]any, key string) string {
	v, _ := p[key].(string)
	return v
}

// payloadInt extracts an int value from the event payload.
// Handles both int and float64 (from JSON unmarshaling).
func payloadInt(p map[string]any, key string) int {
	switch v := p[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /mnt/data/joechenrh/golem && go test ./internal/agent/ -run TestEventAccumulator -v -race`
Expected: All PASS.

- [ ] **Step 5: Run go vet**

Run: `cd /mnt/data/joechenrh/golem && go vet ./internal/agent/...`
Expected: No issues.

- [ ] **Step 6: Commit**

```bash
git add internal/agent/accumulator.go internal/agent/accumulator_test.go
git commit -m "feat: add EventAccumulator hook for progress tracking"
```

---

## Chunk 2: FormatProgress + StatusInfo Extension

### Task 3: Create FormatProgress with tests (TDD)

**Files:**
- Create: `internal/agent/progress_format.go`
- Create: `internal/agent/progress_format_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/agent/progress_format_test.go`:

```go
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
			contains: []string{"implementing error handling"},
		},
		{
			name: "with active tool",
			snap: StatusSnapshot{
				State: SessionState{
					Iteration:   7, MaxIter: 15,
					ActiveTool:  "shell_exec",
					ToolStarted: time.Now().Add(-3 * time.Second),
				},
			},
			contains: []string{"shell_exec", "running"},
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /mnt/data/joechenrh/golem && go test ./internal/agent/ -run TestFormatProgress -v`
Expected: FAIL — `FormatProgress` undefined.

- [ ] **Step 3: Implement FormatProgress**

Create `internal/agent/progress_format.go`:

```go
package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/joechenrh/golem/internal/hooks"
)

// FormatProgress formats a StatusSnapshot into a human-readable progress
// report for the /status slash command.
func FormatProgress(snap StatusSnapshot) string {
	var b strings.Builder

	fmt.Fprintf(&b, "📊 Progress — Iteration %d/%d\n",
		snap.State.Iteration, snap.State.MaxIter)

	if snap.State.Phase != "" {
		fmt.Fprintf(&b, "Phase: %q\n", snap.State.Phase)
	}

	if snap.State.ActiveTool != "" {
		elapsed := time.Since(snap.State.ToolStarted).Truncate(time.Second)
		fmt.Fprintf(&b, "Current: %s running (%s elapsed)\n",
			snap.State.ActiveTool, elapsed)
	}

	if len(snap.RecentEvents) > 0 {
		hasToolEvents := false
		for _, e := range snap.RecentEvents {
			if e.Type == hooks.EventAfterToolExec || e.Type == hooks.EventBeforeToolExec {
				hasToolEvents = true
				break
			}
		}
		if hasToolEvents {
			b.WriteString("\nRecent activity:\n")
			for _, e := range snap.RecentEvents {
				switch e.Type {
				case hooks.EventAfterToolExec:
					name := payloadStr(e.Payload, "tool_name")
					durationMs := payloadInt(e.Payload, "duration_ms")
					errStr := payloadStr(e.Payload, "error")
					if errStr != "" {
						fmt.Fprintf(&b, "  ✗ %s (%dms, error)\n", name, durationMs)
					} else {
						fmt.Fprintf(&b, "  ✓ %s (%dms)\n", name, durationMs)
					}
				case hooks.EventBeforeToolExec:
					name := payloadStr(e.Payload, "tool_name")
					fmt.Fprintf(&b, "  ⟳ %s (running...)\n", name)
				}
			}
		}
	}

	if snap.State.RunningTasks > 0 {
		fmt.Fprintf(&b, "\nBackground tasks: %d running\n", snap.State.RunningTasks)
	}

	return b.String()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /mnt/data/joechenrh/golem && go test ./internal/agent/ -run TestFormatProgress -v`
Expected: All PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/progress_format.go internal/agent/progress_format_test.go
git commit -m "feat: add FormatProgress for /status output"
```

---

### Task 4: Add accumulator field to Session + extend StatusInfo

**Files:**
- Modify: `internal/agent/session.go:37-104` (Session struct)
- Modify: `internal/agent/session.go:148-201` (NewSession)
- Modify: `internal/agent/session.go:428-440` (StatusInfo)

- [ ] **Step 1: Add the accumulator field to Session**

In `internal/agent/session.go`, add the field to the `Session` struct after line 96 (after `prompt *PromptBuilder`):

```go
	// Progress tracking accumulator (nil for CLI sessions without remote channels).
	accumulator *EventAccumulator
```

- [ ] **Step 2: Add the SetAccumulator setter**

After the `SetExtHooks` method (line 214):

```go
// SetAccumulator attaches an EventAccumulator for progress tracking.
func (s *Session) SetAccumulator(acc *EventAccumulator) {
	s.accumulator = acc
}

// Accumulator returns the session's EventAccumulator, or nil if none is set.
func (s *Session) Accumulator() *EventAccumulator {
	return s.accumulator
}
```

- [ ] **Step 3: Extend StatusInfo to include progress**

Replace the existing `StatusInfo()` method (lines 428-440) with:

```go
// StatusInfo returns a human-readable status summary for this session.
// When the session is actively running and has an accumulator, progress
// information is appended to the base model/token info.
func (s *Session) StatusInfo() string {
	model := s.config.Model
	totalTokens := s.sessionUsage.TotalTokens
	promptTokens := s.sessionUsage.PromptTokens
	completionTokens := s.sessionUsage.CompletionTokens
	toolCount := s.tools.Count()

	base := fmt.Sprintf(
		"**Model:** %s\n**Tools:** %d\n**Tokens used:** %d (prompt: %d, completion: %d)",
		model, toolCount, totalTokens, promptTokens, completionTokens,
	)

	if s.accumulator != nil {
		snap := s.accumulator.Snapshot()
		if snap.State.IdleSince == nil && snap.State.Iteration > 0 {
			return base + "\n\n" + FormatProgress(snap)
		}
	}

	return base
}
```

- [ ] **Step 4: Verify the build**

Run: `cd /mnt/data/joechenrh/golem && go build ./internal/agent/...`
Expected: Success.

- [ ] **Step 5: Run existing tests**

Run: `cd /mnt/data/joechenrh/golem && go test ./internal/agent/ -v`
Expected: All PASS (no behavioral change to existing tests).

- [ ] **Step 6: Commit**

```bash
git add internal/agent/session.go
git commit -m "feat: add accumulator to Session and extend StatusInfo with progress"
```

---

## Chunk 3: ProgressReporter + report_progress Tool

### Task 5: Create ProgressReporter with tests (TDD)

**Files:**
- Create: `internal/agent/progress_reporter.go`
- Create: `internal/agent/progress_reporter_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/agent/progress_reporter_test.go`:

```go
package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/joechenrh/golem/internal/hooks"
)

// mockChannel records SendDirect calls for testing.
type mockChannel struct {
	mu       sync.Mutex
	sent     []string
	sendErr  error
}

func (m *mockChannel) SendDirect(_ context.Context, _, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, text)
	return m.sendErr
}

func (m *mockChannel) sentMessages() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.sent...)
}

func TestProgressReporter_SendsPhaseUpdate(t *testing.T) {
	ch := &mockChannel{}
	r := NewProgressReporter(ch, "chat-1", 0, zap.NewNop())

	r.Handle(context.Background(), hooks.Event{
		Type:    hooks.EventPhaseUpdate,
		Payload: map[string]any{"summary": "analyzing codebase"},
	})

	msgs := ch.sentMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0] != "📋 analyzing codebase" {
		t.Errorf("unexpected message: %q", msgs[0])
	}
}

func TestProgressReporter_IgnoresNonPhaseEvents(t *testing.T) {
	ch := &mockChannel{}
	r := NewProgressReporter(ch, "chat-1", 0, zap.NewNop())

	r.Handle(context.Background(), hooks.Event{
		Type:    hooks.EventIterationStart,
		Payload: map[string]any{"iteration": 1},
	})

	msgs := ch.sentMessages()
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages for non-phase event, got %d", len(msgs))
	}
}

func TestProgressReporter_Throttle(t *testing.T) {
	ch := &mockChannel{}
	r := NewProgressReporter(ch, "chat-1", 1*time.Hour, zap.NewNop())

	r.Handle(context.Background(), hooks.Event{
		Type:    hooks.EventPhaseUpdate,
		Payload: map[string]any{"summary": "first"},
	})
	r.Handle(context.Background(), hooks.Event{
		Type:    hooks.EventPhaseUpdate,
		Payload: map[string]any{"summary": "second (throttled)"},
	})

	msgs := ch.sentMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message (second throttled), got %d", len(msgs))
	}
	if msgs[0] != "📋 first" {
		t.Errorf("unexpected message: %q", msgs[0])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /mnt/data/joechenrh/golem && go test ./internal/agent/ -run TestProgressReporter -v`
Expected: FAIL — `NewProgressReporter` undefined.

- [ ] **Step 3: Implement ProgressReporter**

Create `internal/agent/progress_reporter.go`:

```go
package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/joechenrh/golem/internal/hooks"
)

// DirectSender is the subset of channel.Channel needed by ProgressReporter.
// Defined here to avoid importing the channel package.
type DirectSender interface {
	SendDirect(ctx context.Context, chatID, text string) error
}

// ProgressReporter implements hooks.Hook and sends milestone updates
// to a chat channel when phase_update events are emitted.
type ProgressReporter struct {
	channel  DirectSender
	chatID   string
	minGap   time.Duration
	mu       sync.Mutex
	lastSent time.Time
	logger   *zap.Logger
}

// NewProgressReporter creates a reporter that sends milestone updates
// to the given channel/chatID. minGap is the minimum time between messages
// (use 0 to disable throttling, e.g. in tests).
func NewProgressReporter(
	ch DirectSender, chatID string, minGap time.Duration, logger *zap.Logger,
) *ProgressReporter {
	return &ProgressReporter{
		channel: ch,
		chatID:  chatID,
		minGap:  minGap,
		logger:  logger,
	}
}

// Name implements hooks.Hook.
func (r *ProgressReporter) Name() string { return "progress_reporter" }

// Handle implements hooks.Hook. Only acts on phase_update events.
func (r *ProgressReporter) Handle(ctx context.Context, event hooks.Event) error {
	if event.Type != hooks.EventPhaseUpdate {
		return nil
	}

	r.mu.Lock()
	if r.minGap > 0 && time.Since(r.lastSent) < r.minGap {
		r.mu.Unlock()
		return nil
	}
	r.lastSent = time.Now()
	r.mu.Unlock()

	summary, _ := event.Payload["summary"].(string)
	if err := r.channel.SendDirect(ctx, r.chatID, fmt.Sprintf("📋 %s", summary)); err != nil {
		r.logger.Warn("failed to send progress update",
			zap.String("chat_id", r.chatID), zap.Error(err))
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /mnt/data/joechenrh/golem && go test ./internal/agent/ -run TestProgressReporter -v -race`
Expected: All PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/progress_reporter.go internal/agent/progress_reporter_test.go
git commit -m "feat: add ProgressReporter hook for chat milestone updates"
```

---

### Task 6: Create report_progress tool with tests (TDD)

**Files:**
- Create: `internal/tools/builtin/progress_tool.go`
- Create: `internal/tools/builtin/progress_tool_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/tools/builtin/progress_tool_test.go`:

```go
package builtin

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"github.com/joechenrh/golem/internal/hooks"
)

func TestReportProgressTool(t *testing.T) {
	bus := hooks.NewBus(zap.NewNop())

	// Capture emitted events.
	var captured []hooks.Event
	bus.Register(&captureHook{fn: func(e hooks.Event) { captured = append(captured, e) }})

	tool := NewReportProgressTool(bus)

	t.Run("emits phase_update event", func(t *testing.T) {
		captured = nil
		result, err := tool.Execute(context.Background(), `{"summary":"analyzing code"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "Progress reported." {
			t.Errorf("unexpected result: %q", result)
		}
		if len(captured) != 1 {
			t.Fatalf("expected 1 event, got %d", len(captured))
		}
		if captured[0].Type != hooks.EventPhaseUpdate {
			t.Errorf("expected phase_update event, got %q", captured[0].Type)
		}
		summary, _ := captured[0].Payload["summary"].(string)
		if summary != "analyzing code" {
			t.Errorf("expected summary %q, got %q", "analyzing code", summary)
		}
	})

	t.Run("requires summary", func(t *testing.T) {
		result, err := tool.Execute(context.Background(), `{}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "Error: 'summary' is required" {
			t.Errorf("unexpected result: %q", result)
		}
	})
}

// captureHook captures all events for testing.
type captureHook struct {
	fn func(hooks.Event)
}

func (h *captureHook) Name() string { return "capture" }
func (h *captureHook) Handle(_ context.Context, e hooks.Event) error {
	h.fn(e)
	return nil
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /mnt/data/joechenrh/golem && go test ./internal/tools/builtin/ -run TestReportProgressTool -v`
Expected: FAIL — `NewReportProgressTool` undefined.

- [ ] **Step 3: Implement report_progress tool**

Create `internal/tools/builtin/progress_tool.go`:

```go
package builtin

import (
	"context"
	"encoding/json"

	"github.com/joechenrh/golem/internal/hooks"
	"github.com/joechenrh/golem/internal/tools"
)

// ReportProgressTool allows the LLM to send milestone progress updates
// to the user via the hook bus. Only registered on remote channels.
type ReportProgressTool struct {
	hooks *hooks.Bus
}

// NewReportProgressTool creates a progress reporting tool.
func NewReportProgressTool(hookBus *hooks.Bus) *ReportProgressTool {
	return &ReportProgressTool{hooks: hookBus}
}

func (t *ReportProgressTool) Name() string        { return "report_progress" }
func (t *ReportProgressTool) Description() string  { return "Report a milestone progress update" }
func (t *ReportProgressTool) FullDescription() string {
	return "Report a milestone progress update to keep the user informed " +
		"about your progress on multi-step tasks. Call this at natural phase " +
		"transitions (e.g., after analysis, before implementation, after completing " +
		"a major subtask). Do NOT call on every tool use — only at meaningful milestones."
}

var reportProgressParams = json.RawMessage(`{
	"type": "object",
	"properties": {
		"summary": {
			"type": "string",
			"description": "A 1-2 sentence milestone update describing what was completed or what is starting next."
		}
	},
	"required": ["summary"]
}`)

func (t *ReportProgressTool) Parameters() json.RawMessage { return reportProgressParams }

func (t *ReportProgressTool) Execute(
	ctx context.Context, args string,
) (string, error) {
	var params struct {
		Summary string `json:"summary"`
	}
	if errMsg := tools.ParseArgs(args, &params); errMsg != "" {
		return errMsg, nil
	}
	if params.Summary == "" {
		return "Error: 'summary' is required", nil
	}

	t.hooks.Emit(ctx, hooks.Event{
		Type:    hooks.EventPhaseUpdate,
		Payload: map[string]any{"summary": params.Summary},
	})

	return "Progress reported.", nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /mnt/data/joechenrh/golem && go test ./internal/tools/builtin/ -run TestReportProgressTool -v`
Expected: All PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tools/builtin/progress_tool.go internal/tools/builtin/progress_tool_test.go
git commit -m "feat: add report_progress tool for LLM milestone updates"
```

---

## Chunk 3: ReAct Loop + TaskTracker Integration

### Task 7: Emit events from the ReAct loop

**Files:**
- Modify: `internal/agent/react.go:22-226`

- [ ] **Step 1: Add turn_start emit + defer turn_done at top of runReActLoop**

In `internal/agent/react.go`, after line 55 (`s.cachedSystemPrompt = s.prompt.Build()`) and before line 56, add:

```go
	// Emit turn_start for progress tracking.
	if s.accumulator != nil {
		s.hooks.Emit(ctx, hooks.Event{
			Type:    hooks.EventTurnStart,
			Payload: map[string]any{"user_message": truncateForPayload(pendingMsg)},
		})
		defer func() {
			s.hooks.Emit(ctx, hooks.Event{
				Type:    hooks.EventTurnDone,
				Payload: map[string]any{"tokens_used": s.turnUsage.TotalTokens},
			})
		}()
	}
```

- [ ] **Step 2: Add iteration_start emit inside the loop**

In `react.go`, after line 74 (`for iter < s.config.MaxToolIter {`) and before line 75, add:

```go
		// Emit iteration_start for progress tracking.
		if s.accumulator != nil {
			s.hooks.Emit(ctx, hooks.Event{
				Type: hooks.EventIterationStart,
				Payload: map[string]any{
					"iteration": iter,
					"max_iter":  s.config.MaxToolIter,
				},
			})
		}
```

- [ ] **Step 3: Add the truncateForPayload helper**

At the bottom of `react.go` (after `lastUserMessage`), add:

```go
// truncateForPayload returns a truncated user message for event payloads.
func truncateForPayload(msg *IncomingMessage) string {
	if msg == nil {
		return ""
	}
	if len(msg.Text) > 200 {
		return msg.Text[:200] + "…"
	}
	return msg.Text
}
```

- [ ] **Step 4: Verify the build**

Run: `cd /mnt/data/joechenrh/golem && go build ./internal/agent/...`
Expected: Success.

- [ ] **Step 5: Run existing tests**

Run: `cd /mnt/data/joechenrh/golem && go test ./internal/agent/ -v`
Expected: All PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/agent/react.go
git commit -m "feat: emit progress events from ReAct loop"
```

---

### Task 8: Add hooks.Bus to TaskTracker + emit task events

**Files:**
- Modify: `internal/agent/tasks.go:48-56` (TaskTracker struct)
- Modify: `internal/agent/tasks.go:60-67` (NewTaskTracker)
- Modify: `internal/agent/tasks.go:88-98` (Launch)
- Modify: `internal/agent/tasks.go:100-110` (Complete)
- Modify: `internal/agent/tasks.go:112-122` (Fail)

- [ ] **Step 1: Add hooks field to TaskTracker**

In `internal/agent/tasks.go`, modify the `TaskTracker` struct (line 50-56) to add a `hooks` field:

```go
type TaskTracker struct {
	mu    sync.Mutex
	tasks map[int]*BackgroundTask
	seq   int
	g     errgroup.Group
	done  chan struct{}
	hooks *hooks.Bus // optional, nil for sub-agent trackers
}
```

Add these imports at the top of `tasks.go`:

```go
	"github.com/joechenrh/golem/internal/hooks"
	"github.com/joechenrh/golem/internal/stringutil"
```

- [ ] **Step 2: Add SetHooks setter**

After `NewTaskTracker` (line 67), add:

```go
// SetHooks attaches a hook bus for emitting task lifecycle events.
func (tt *TaskTracker) SetHooks(bus *hooks.Bus) {
	tt.hooks = bus
}
```

- [ ] **Step 3: Emit task_launched in Launch**

In `Launch()`, after `id := tt.Add(desc, cancel)` (line 92) and before `tt.g.Go` (line 93), add:

```go
	if tt.hooks != nil {
		tt.hooks.Emit(context.Background(), hooks.Event{
			Type: hooks.EventTaskLaunched,
			Payload: map[string]any{
				"task_id":     id,
				"description": desc,
			},
		})
	}
```

- [ ] **Step 4: Emit task_completed in Complete**

Replace the entire `Complete` method (lines 101-110). The emit goes after the lock is released to avoid holding the lock during hook dispatch. Capture `desc` while the lock is held:

```go
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
```

- [ ] **Step 5: Emit task_completed in Fail**

Replace the entire `Fail` method:

```go
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
```

- [ ] **Step 6: Verify the build**

Run: `cd /mnt/data/joechenrh/golem && go build ./internal/agent/...`
Expected: Success.

- [ ] **Step 7: Run existing tests**

Run: `cd /mnt/data/joechenrh/golem && go test ./internal/agent/ -v -race`
Expected: All PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/agent/tasks.go
git commit -m "feat: emit task lifecycle events from TaskTracker"
```

---

## Chunk 4: Wiring

### Task 9: Wire accumulator + reporter in buildDefaultSession

**Files:**
- Modify: `internal/app/app.go:475-502` (buildDefaultSession)

- [ ] **Step 1: Update buildDefaultSession to accept and wire accumulator**

In `internal/app/app.go`, modify `buildDefaultSession` to create and wire the accumulator. After session creation (line 496) and before return (line 501), add:

```go
	// Wire progress tracking for remote channels.
	accumulator := agent.NewEventAccumulator(50)
	hookBus.Register(accumulator)
	sess.SetAccumulator(accumulator)
	sess.Tasks().SetHooks(hookBus)
```

This requires a `Tasks()` accessor on Session. Add it to `session.go` near `SetAccumulator`:

```go
// Tasks returns the session's TaskTracker.
func (s *Session) Tasks() *TaskTracker {
	return s.tasks
}
```

- [ ] **Step 2: Verify the build**

Run: `cd /mnt/data/joechenrh/golem && go build ./...`
Expected: Success.

- [ ] **Step 3: Commit**

```bash
git add internal/app/app.go internal/agent/session.go
git commit -m "feat: wire EventAccumulator into default session"
```

---

### Task 10: Wire accumulator + reporter + progress tool in managed sessions

**Files:**
- Modify: `internal/agent/manager.go:22-35` (SessionFactory struct)
- Modify: `internal/agent/manager.go:141-198` (createSession)
- Modify: `internal/app/app.go:504-532` (buildSessionManager)

- [ ] **Step 1: Add Channel field to SessionFactory**

In `internal/agent/manager.go`, add a `Channel` field to `SessionFactory` (after line 34):

```go
	Channel       DirectSender      // chat channel for progress updates (nil if none)
```

- [ ] **Step 2: Wire accumulator + reporter + progress tool in createSession**

In `createSession()`, after the `hookBus` creation (line 172) and before `registry := sm.factory.ToolFactory()` (line 174), add:

```go
	// Wire progress tracking.
	accumulator := NewEventAccumulator(50)
	hookBus.Register(accumulator)
	if sm.factory.Channel != nil {
		reporter := NewProgressReporter(sm.factory.Channel, channelID, 10*time.Second, sm.logger)
		hookBus.Register(reporter)
	}
```

After session creation (line 189), before return (line 197), add:

```go
	sess.SetAccumulator(accumulator)
	sess.Tasks().SetHooks(hookBus)
```

Also register the `report_progress` tool. After `registry := sm.factory.ToolFactory()` (line 174), add:

```go
	if sm.factory.Channel != nil {
		registry.Register(builtin.NewReportProgressTool(hookBus))
		registry.Expand("report_progress")
	}
```

Add the import for `"github.com/joechenrh/golem/internal/tools/builtin"`.

- [ ] **Step 2b: Wire progress tracking in createSessionFromTape**

In `createSessionFromTape()` (manager.go lines 200-230), add the same wiring after session creation (line 223), before `sess.TapePath = tapePath` (line 224):

```go
	// Wire progress tracking (same as createSession).
	accumulator := NewEventAccumulator(50)
	hookBus.Register(accumulator)
	if sm.factory.Channel != nil {
		reporter := NewProgressReporter(sm.factory.Channel, "restored", 10*time.Second, sm.logger)
		hookBus.Register(reporter)
		registry.Register(builtin.NewReportProgressTool(hookBus))
		registry.Expand("report_progress")
	}
```

After `sess := NewSession(...)` (line 223), add:

```go
	sess.SetAccumulator(accumulator)
	sess.Tasks().SetHooks(hookBus)
```

- [ ] **Step 3: Pass channel to SessionFactory in buildSessionManager**

In `internal/app/app.go`, modify `buildSessionManager` to accept and pass the channel. Update the function signature (line 505) to accept `ch DirectSender`:

Actually, since `buildSessionManager` doesn't currently receive a channel reference, and `SessionFactory` needs one, we need to pass it. Looking at the call site (line 401):

```go
sessions = buildSessionManager(name, cfg, llmClient, classifierLLM,
    spawnToolFactory, metricsHook, agentTapeDir, skillDirs, extHookRunner, logger)
```

The Lark channel is available as `larkCh` at this point. Add a parameter:

Update `buildSessionManager` signature to:

```go
func buildSessionManager(
	name string, cfg *config.Config,
	llmClient, classifierLLM llm.Client,
	toolFactory func() *tools.Registry,
	metricsHook *hooks.MetricsHook,
	auditDir string,
	skillDirs []string,
	extHookRunner agent.ExtHookRunner,
	logger *zap.Logger,
	ch agent.DirectSender,
) *agent.SessionManager {
```

And add `Channel: ch` to the `SessionFactory` initialization.

Update the call site (line 401) to pass `larkCh`:

```go
// larkCh implements DirectSender via SendDirect.
// Cast to DirectSender (it may be nil, which is fine — createSession checks).
var remoteSender agent.DirectSender
if larkCh != nil {
    remoteSender = larkCh
}
sessions = buildSessionManager(name, cfg, llmClient, classifierLLM,
    spawnToolFactory, metricsHook, agentTapeDir, skillDirs, extHookRunner, logger, remoteSender)
```

- [ ] **Step 4: Register report_progress in default session for remote channels**

In `buildDefaultSession` (already modified in Task 9), after the accumulator wiring, add:

```go
	// Register report_progress tool for remote channels.
	// The caller passes hasRemoteChannels based on config.
```

Actually, `buildDefaultSession` doesn't know if there are remote channels. The simplest approach: always register the tool (it's harmless on CLI — the LLM won't call it if the system prompt doesn't mention it). But per spec, we only register on remote channels.

Add a `hasRemote bool` parameter to `buildDefaultSession`:

Update signature:

```go
func buildDefaultSession(
	name string,
	cfg *config.Config,
	llmClient, classifierLLM llm.Client,
	toolFactory func() *tools.Registry,
	tapeStore tape.Store,
	ctxStrategy ctxmgr.ContextStrategy,
	hookBus *hooks.Bus,
	metricsHook *hooks.MetricsHook,
	skillDirs []string,
	extHookRunner agent.ExtHookRunner,
	logger *zap.Logger,
	hasRemoteChannels bool,
) (*tools.Registry, *agent.Session) {
```

After accumulator wiring:

```go
	if hasRemoteChannels {
		registry.Register(builtin.NewReportProgressTool(hookBus))
		registry.Expand("report_progress")
	}
```

Update the call site (line 377):

```go
	registry, defaultSess := buildDefaultSession(
		name, cfg, llmClient, classifierLLM,
		spawnToolFactory, tapeStore, ctxStrategy, hookBus, metricsHook,
		skillDirs, extHookRunner, logger,
		cfg.HasRemoteChannels(),
	)
```

- [ ] **Step 5: Verify the build**

Run: `cd /mnt/data/joechenrh/golem && go build ./...`
Expected: Success.

- [ ] **Step 6: Run all tests**

Run: `cd /mnt/data/joechenrh/golem && go test ./... -race`
Expected: All PASS.

- [ ] **Step 7: Run go vet**

Run: `cd /mnt/data/joechenrh/golem && go vet ./...`
Expected: No issues.

- [ ] **Step 8: Commit**

```bash
git add internal/agent/manager.go internal/app/app.go internal/agent/session.go
git commit -m "feat: wire progress tracking into managed sessions and default session"
```

---

## Chunk 5: System Prompt + Design Doc Updates

### Task 11: Add progress reporting instruction to system prompt

**Files:**
- Modify: `internal/agent/session.go:129-140` (toolUseInstruction or PromptBuilder)

- [ ] **Step 1: Add the progress reporting instruction to both prompt paths**

The system prompt is assembled in `internal/agent/prompt_builder.go` via two paths:
- `buildPersonaPrompt()` — adds `toolUseInstruction` at line 117
- `buildFlatPrompt()` — adds `toolUseInstruction` at line 157

The progress instruction must be added **after** `toolUseInstruction` in **both** paths, conditional on `report_progress` being registered. Use `slices.Contains(pb.tools.Names(), "report_progress")` since `Registry` has `Names()` but not `Has()`.

In `buildPersonaPrompt()`, after line 117 (`b.WriteString(toolUseInstruction)`), add:

```go
	if slices.Contains(pb.tools.Names(), "report_progress") {
		b.WriteString("\n## Progress Reporting\n\n")
		b.WriteString("When working on multi-step tasks, use report_progress to keep the user ")
		b.WriteString("informed at natural milestones:\n")
		b.WriteString("• After finishing analysis/planning\n")
		b.WriteString("• Before starting implementation\n")
		b.WriteString("• After completing a major subtask\n")
		b.WriteString("• When encountering a significant blocker or change of approach\n\n")
		b.WriteString("Do NOT call it on every tool use. Only at meaningful phase transitions.\n")
	}
```

In `buildFlatPrompt()`, after line 157 (`b.WriteString(toolUseInstruction)`), add the same block.

Add the `"slices"` import to the file.

- [ ] **Step 2: Verify the build**

Run: `cd /mnt/data/joechenrh/golem && go build ./internal/agent/...`
Expected: Success.

- [ ] **Step 3: Commit**

```bash
git add internal/agent/prompt_builder.go
git commit -m "feat: add progress reporting instruction to system prompt"
```

---

### Task 12: Update design docs

Per CLAUDE.md: "When changing a subsystem's behavior, update the corresponding `design/*.md` file."

**Files:**
- Modify: `design/08-hooks.md`
- Modify: `design/02-agent-session.md`

- [ ] **Step 1: Update hooks design doc**

Add a section to `design/08-hooks.md` documenting the 6 new event types and the progress tracking hooks (EventAccumulator, ProgressReporter).

- [ ] **Step 2: Update agent-session design doc**

Add a section to `design/02-agent-session.md` documenting the accumulator field, the extended StatusInfo, and the event emission points in the ReAct loop.

- [ ] **Step 3: Commit**

```bash
git add design/08-hooks.md design/02-agent-session.md
git commit -m "docs: update design docs for progress tracking hooks and session changes"
```

---

## Chunk 6: Integration Test

### Task 13: Integration test — full progress pipeline

**Files:**
- Create: `internal/agent/progress_integration_test.go`

- [ ] **Step 1: Write integration test**

Create `internal/agent/progress_integration_test.go` that:
1. Creates a real `hooks.Bus`
2. Registers an `EventAccumulator` and a `ProgressReporter` (with a mock channel)
3. Emits a sequence of events simulating a ReAct loop (turn_start, iteration_start, before_tool_exec, after_tool_exec, phase_update, turn_done)
4. Verifies the accumulator snapshot at each stage
5. Verifies the reporter sent exactly the expected messages

```go
package agent

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"github.com/joechenrh/golem/internal/hooks"
)

func TestProgressPipeline_Integration(t *testing.T) {
	bus := hooks.NewBus(zap.NewNop())
	acc := NewEventAccumulator(50)
	bus.Register(acc)

	ch := &mockChannel{}
	reporter := NewProgressReporter(ch, "test-chat", 0, zap.NewNop())
	bus.Register(reporter)

	ctx := context.Background()

	// Simulate a ReAct loop.
	bus.Emit(ctx, hooks.Event{Type: hooks.EventTurnStart, Payload: map[string]any{"user_message": "fix the bug"}})

	snap := acc.Snapshot()
	if snap.State.IdleSince != nil {
		t.Error("should not be idle after turn_start")
	}

	bus.Emit(ctx, hooks.Event{Type: hooks.EventIterationStart, Payload: map[string]any{"iteration": 0, "max_iter": 15}})

	bus.Emit(ctx, hooks.Event{Type: hooks.EventBeforeToolExec, Payload: map[string]any{"tool_name": "read_file"}})
	snap = acc.Snapshot()
	if snap.State.ActiveTool != "read_file" {
		t.Errorf("expected active tool read_file, got %q", snap.State.ActiveTool)
	}

	bus.Emit(ctx, hooks.Event{Type: hooks.EventAfterToolExec, Payload: map[string]any{"tool_name": "read_file", "duration_ms": int64(50)}})
	snap = acc.Snapshot()
	if snap.State.ActiveTool != "" {
		t.Errorf("expected no active tool, got %q", snap.State.ActiveTool)
	}

	// Phase update — should trigger reporter.
	bus.Emit(ctx, hooks.Event{Type: hooks.EventPhaseUpdate, Payload: map[string]any{"summary": "found the root cause"}})
	snap = acc.Snapshot()
	if snap.State.Phase != "found the root cause" {
		t.Errorf("expected phase %q, got %q", "found the root cause", snap.State.Phase)
	}

	msgs := ch.sentMessages()
	if len(msgs) != 1 || msgs[0] != "📋 found the root cause" {
		t.Errorf("expected 1 reporter message, got %v", msgs)
	}

	// Turn done.
	bus.Emit(ctx, hooks.Event{Type: hooks.EventTurnDone, Payload: map[string]any{"tokens_used": 500}})
	snap = acc.Snapshot()
	if snap.State.IdleSince == nil {
		t.Error("expected idle after turn_done")
	}
}
```

- [ ] **Step 2: Run the integration test**

Run: `cd /mnt/data/joechenrh/golem && go test ./internal/agent/ -run TestProgressPipeline -v -race`
Expected: PASS.

- [ ] **Step 3: Run the full test suite**

Run: `cd /mnt/data/joechenrh/golem && go test ./... -race`
Expected: All PASS.

- [ ] **Step 4: Run gofmt check**

Run: `cd /mnt/data/joechenrh/golem && gofmt -d .`
Expected: No diff.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/progress_integration_test.go
git commit -m "test: add integration test for progress tracking pipeline"
```
