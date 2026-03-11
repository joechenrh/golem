# Observable Progress for Long-Running Agent Tasks

**Date:** 2026-03-11
**Status:** Approved

## Problem

When a user sends a task to a Golem agent via a remote channel (Lark/Telegram), the agent goes silent while executing a potentially long ReAct loop. The user has no visibility into what's happening and no way to check progress without disrupting the workflow.

## Goals

1. **Milestone-level progress updates** — the LLM reports at natural phase transitions (pushed to chat automatically)
2. **On-demand status queries** — the user sends `/status` and gets current state + recent activity without interrupting the ReAct loop
3. **Tool-level visibility** — available via `/status`, not pushed automatically (avoids chat spam)
4. **Non-disruptive** — progress observation never interferes with the running agent

## Non-Goals

- Resource management, prioritization, or quotas
- Hierarchical subagent spawning
- User message injection into the running ReAct loop (steer)
- CLI progress display (CLI already shows streaming output)

## Architecture

### Design Approach: Extend the Existing Hook Bus

Rather than introducing a parallel event system, we extend the existing `hooks.Bus` and `hooks.Event` with new event types. The `EventAccumulator` is implemented as a `hooks.Hook` that registers on the bus, accumulates events, and exposes a `Snapshot()` method for the `StatusHandler`.

This leverages the existing infrastructure:
- The hook bus already fires `before_tool_exec` and `after_tool_exec` events with tool name, arguments, duration, and result.
- The bus is already thread-safe (`sync.RWMutex`).
- The `Hook.Handle(ctx, event)` signature already receives a `context.Context`.

### Components

```
  Lark / Telegram (Channel)
       │ incoming msg              ▲ outgoing msg
       ▼                           │
  ┌──────────────┐        ┌───────────────────┐
  │ /status      │        │ ProgressReporter  │
  │ handler in   │        │ (hooks.Hook)      │
  │ existing     │        │                   │
  │ slash-cmd    ├──┐     │ Formats milestone │
  │ switch       │  │     │ updates for chat  │
  └──────────────┘  │     └────────▲──────────┘
                    │              │
                    │              │ registered on hooks.Bus
                    │              │ handles phase_update events
                    ▼              │
            ┌──────────────┐   ┌──┴─────────────────┐
            │ StatusHandler│──►│  EventAccumulator   │
            │              │   │  (hooks.Hook)       │
            │ Pure read,   │   │                     │
            │ no ReAct     │   │  Handles:           │
            │ disruption   │   │  • before_tool_exec │
            └──────────────┘   │  • after_tool_exec  │
                               │  • iteration_start  │
                               │  • phase_update     │
                               │  • task_launched     │
                               │  • task_completed   │
                               │  • turn_start/done  │
                               └─────────────────────┘
```

**EventAccumulator** (implements `hooks.Hook`) — registered on the existing `hooks.Bus`. Handles both existing event types (`before_tool_exec`, `after_tool_exec`) and new ones (`iteration_start`, `phase_update`, `turn_start`, `turn_done`, `task_launched`, `task_completed`). Maintains a rolling window of recent events (default 50) and a live `SessionState` struct. Thread-safe via its own RWMutex for the `Snapshot()` read path.

**StatusHandler** — reads an EventAccumulator snapshot and formats it as a status message. Pure read operation — acquires a read lock on the accumulator, copies the snapshot, releases. Never touches the ReAct loop. Returns: current iteration, active tool with elapsed time, last phase summary, last 3-5 tool results, and background task status.

**ProgressReporter** (implements `hooks.Hook`) — registered on the same `hooks.Bus`. Only handles `phase_update` events. When the LLM calls the `report_progress` tool, the reporter formats the milestone summary and sends it to the chat channel. Has a 10-second minimum gap between messages as a throttle against spam. Receives `context.Context` via the standard `Hook.Handle(ctx, event)` signature.

### `/status` Command — Extending the Existing Handler

The existing `/status` slash command in `app.go:handleSlashCommand` already shows model/token info via `sess.StatusInfo()`. We extend `StatusInfo()` to include progress information when the session is actively running, rather than adding a new routing layer.

When idle, `/status` returns the existing model/token summary. When active (accumulator has events), it appends the progress snapshot below the existing info:

```
**Model:** gpt-4o
**Tools:** 21
**Tokens used:** 12450 (prompt: 9200, completion: 3250)

📊 Progress — Iteration 7/15
Phase: "Implementing error handling for the API layer"

Recent activity:
  ✓ read_file  api/handler.go         (0.1s)
  ✓ search_files "handleError"        (0.3s)
  ✓ edit_file  api/handler.go         (0.1s)
  ✓ shell_exec go build ./...         (2.1s, exit 0)
  ⟳ shell_exec go test ./api/...      (4s, running...)

Background tasks: 1 running
  ⟳ #1 "Refactor logging package" — iteration 3/50
```

This avoids introducing a new routing layer and keeps all slash commands in one place.

### Event Types — Extending hooks.EventType

New event types added to the existing `hooks.EventType` constants in `internal/hooks/hooks.go`:

```go
// Existing events (unchanged):
//   EventUserMessage    = "user_message"
//   EventBeforeLLMCall  = "before_llm_call"
//   EventAfterLLMCall   = "after_llm_call"
//   EventBeforeToolExec = "before_tool_exec"
//   EventAfterToolExec  = "after_tool_exec"
//   EventError          = "error"

// New events for progress tracking:
const (
    EventIterationStart EventType = "iteration_start"
    EventPhaseUpdate    EventType = "phase_update"
    EventTaskLaunched   EventType = "task_launched"
    EventTaskCompleted  EventType = "task_completed"
    EventTurnStart      EventType = "turn_start"
    EventTurnDone       EventType = "turn_done"
)
```

These use the existing `hooks.Event` struct with its `map[string]any` payload. No new types needed — the accumulator parses the payload maps.

**Payload conventions for new events:**

| Event | Payload keys |
|-------|-------------|
| `iteration_start` | `iteration` (int), `max_iter` (int) |
| `phase_update` | `summary` (string) |
| `task_launched` | `task_id` (int), `description` (string) |
| `task_completed` | `task_id` (int), `description` (string), `result` (string), `error` (string) |
| `turn_start` | `user_message` (string, truncated to 200 chars) |
| `turn_done` | `tokens_used` (int) |

**Existing events already captured by the accumulator:**

| Event | Payload keys (already defined) |
|-------|-------------------------------|
| `before_tool_exec` | `tool_name`, `tool_id`, `arguments`, `session_id` |
| `after_tool_exec` | `tool_name`, `tool_id`, `result`, `duration_ms`, `arguments`, `session_id` |

### EventAccumulator

```go
// internal/agent/accumulator.go

// EventAccumulator implements hooks.Hook and accumulates lifecycle events
// for progress tracking. It is registered on the hooks.Bus and handles
// both existing tool events and new progress events.
type EventAccumulator struct {
    mu        sync.RWMutex
    events    []accEvent   // rolling window
    maxEvents int          // default: 50
    current   SessionState
}

type accEvent struct {
    Type      hooks.EventType
    Timestamp time.Time
    Payload   map[string]any
}

type SessionState struct {
    Iteration    int
    MaxIter      int
    ActiveTool   string     // empty if idle
    ToolStarted  time.Time  // when ActiveTool started
    Phase        string     // last phase summary
    RunningTasks int
    IdleSince    *time.Time // nil if busy
}

type StatusSnapshot struct {
    State        SessionState
    RecentEvents []accEvent // last 5
}

// Name implements hooks.Hook.
func (a *EventAccumulator) Name() string { return "accumulator" }

// Handle implements hooks.Hook. Called by the hooks.Bus for every event.
func (a *EventAccumulator) Handle(ctx context.Context, event hooks.Event) error {
    a.mu.Lock()
    defer a.mu.Unlock()

    entry := accEvent{
        Type:      event.Type,
        Timestamp: time.Now(),
        Payload:   event.Payload,
    }

    // Rolling window eviction
    if len(a.events) >= a.maxEvents {
        a.events = a.events[1:]
    }
    a.events = append(a.events, entry)

    // Update SessionState based on event type
    switch event.Type {
    case hooks.EventTurnStart:
        a.reset() // clear stale data from previous turns
        a.current.IdleSince = nil
    case hooks.EventBeforeToolExec:
        a.current.ActiveTool = str(event.Payload, "tool_name")
        a.current.ToolStarted = time.Now()
        a.current.IdleSince = nil
    case hooks.EventAfterToolExec:
        a.current.ActiveTool = ""
    case hooks.EventIterationStart:
        a.current.Iteration = intVal(event.Payload, "iteration")
        a.current.MaxIter = intVal(event.Payload, "max_iter")
        a.current.IdleSince = nil
    case hooks.EventPhaseUpdate:
        a.current.Phase = str(event.Payload, "summary")
    case hooks.EventTaskLaunched:
        a.current.RunningTasks++
    case hooks.EventTaskCompleted:
        a.current.RunningTasks = max(0, a.current.RunningTasks-1)
    case hooks.EventTurnDone:
        now := time.Now()
        a.current.IdleSince = &now
        a.current.ActiveTool = ""
    }

    return nil // never blocks
}

// Snapshot returns a copy of the current state + recent events.
// Safe to call concurrently from the StatusHandler goroutine.
func (a *EventAccumulator) Snapshot() StatusSnapshot {
    a.mu.RLock()
    defer a.mu.RUnlock()

    n := min(5, len(a.events))
    recent := make([]accEvent, n)
    copy(recent, a.events[len(a.events)-n:])

    return StatusSnapshot{
        State:        a.current, // value copy
        RecentEvents: recent,
    }
}

// reset clears accumulated events and state. Called internally
// when a turn_start event is received. Caller must hold the write lock.
func (a *EventAccumulator) reset() {
    a.events = a.events[:0]
    a.current = SessionState{}
}
```

### report_progress Tool

```go
// internal/tools/builtin/progress_tool.go

// Schema:
//   name: report_progress
//   parameters:
//     summary: string (required) — a 1-2 sentence milestone update
//   returns: "Progress reported."
//
// Side effect: emits a phase_update event via the hooks.Bus.

func (t *ReportProgressTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
    var params struct{ Summary string }
    if err := json.Unmarshal(args, &params); err != nil {
        return "", fmt.Errorf("invalid args: %w", err)
    }

    // The tool has access to the hooks.Bus (injected at construction).
    // Emit fires synchronously; the EventAccumulator and ProgressReporter
    // both handle it via their Hook.Handle methods.
    t.hooks.Emit(ctx, hooks.Event{
        Type:    hooks.EventPhaseUpdate,
        Payload: map[string]any{"summary": params.Summary},
    })

    return "Progress reported.", nil
}
```

Only registered on remote channels (Lark/Telegram). On CLI, the tool is not registered (the user already sees streaming output).

System prompt addition for remote channels:

```
## Progress Reporting

When working on multi-step tasks, use report_progress to keep the user
informed at natural milestones:

• After finishing analysis/planning
• Before starting implementation
• After completing a major subtask
• When encountering a significant blocker or change of approach

Do NOT call it on every tool use. Only at meaningful phase transitions.
```

### ProgressReporter

```go
// internal/agent/progress_reporter.go

// ProgressReporter implements hooks.Hook and sends milestone updates
// to the chat channel when phase_update events are emitted.
type ProgressReporter struct {
    channel  channel.Channel
    chatID   string
    minGap   time.Duration // default: 10s
    mu       sync.Mutex    // guards lastSent
    lastSent time.Time
    logger   *zap.Logger
}

// Name implements hooks.Hook.
func (r *ProgressReporter) Name() string { return "progress_reporter" }

// Handle implements hooks.Hook. Only acts on phase_update events.
func (r *ProgressReporter) Handle(ctx context.Context, event hooks.Event) error {
    if event.Type != hooks.EventPhaseUpdate {
        return nil
    }

    r.mu.Lock()
    if time.Since(r.lastSent) < r.minGap {
        r.mu.Unlock()
        return nil // throttle
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

Thread safety: `lastSent` is guarded by its own `sync.Mutex` since the `Handle` method is called from the ReAct loop goroutine (via `hooks.Bus.Emit`), and the reporter could theoretically be accessed from shutdown paths.

### StatusHandler

```go
// internal/agent/status_handler.go

// FormatProgress formats the progress section of the /status output.
// Called from Session.StatusInfo() when the accumulator has events.
func FormatProgress(snap StatusSnapshot) string {
    var b strings.Builder

    fmt.Fprintf(&b, "📊 Progress — Iteration %d/%d\n",
        snap.State.Iteration, snap.State.MaxIter)

    if snap.State.Phase != "" {
        fmt.Fprintf(&b, "Phase: %q\n", snap.State.Phase)
    }

    if snap.State.ActiveTool != "" {
        elapsed := time.Since(snap.State.ToolStarted).Truncate(time.Second)
        fmt.Fprintf(&b, "Current: Running %s (%s elapsed)\n",
            snap.State.ActiveTool, elapsed)
    }

    if len(snap.RecentEvents) > 0 {
        b.WriteString("\nRecent activity:\n")
        for _, e := range snap.RecentEvents {
            switch e.Type {
            case hooks.EventAfterToolExec:
                name, _ := e.Payload["tool_name"].(string)
                durationMs, _ := e.Payload["duration_ms"].(int64)
                errStr, _ := e.Payload["error"].(string)
                if errStr != "" {
                    fmt.Fprintf(&b, "  ✗ %s (%dms, error)\n", name, durationMs)
                } else {
                    fmt.Fprintf(&b, "  ✓ %s (%dms)\n", name, durationMs)
                }
            case hooks.EventBeforeToolExec:
                name, _ := e.Payload["tool_name"].(string)
                fmt.Fprintf(&b, "  ⟳ %s (running...)\n", name)
            }
        }
    }

    if snap.State.RunningTasks > 0 {
        fmt.Fprintf(&b, "\nBackground tasks: %d running\n", snap.State.RunningTasks)
    }

    return b.String()
}
```

The existing `Session.StatusInfo()` method is extended:

```go
func (s *Session) StatusInfo() string {
    // Existing model/token info
    base := fmt.Sprintf(
        "**Model:** %s\n**Tools:** %d\n**Tokens used:** %d (prompt: %d, completion: %d)",
        s.config.Model, s.tools.Count(), ...)

    // Append progress info if accumulator exists and session is active
    if s.accumulator != nil {
        snap := s.accumulator.Snapshot()
        if snap.State.IdleSince == nil && snap.State.Iteration > 0 {
            return base + "\n\n" + FormatProgress(snap)
        }
    }

    return base
}
```

## Integration Points

### Event emission in existing code

All events flow through the existing `hooks.Bus.Emit()` call. No new dispatch mechanism.

| Location | Event | How |
|----------|-------|-----|
| `react.go` — top of `runReActLoop()` | `turn_start` | `s.hooks.Emit(ctx, hooks.Event{Type: hooks.EventTurnStart, ...})` |
| `react.go` — loop body top | `iteration_start` | `s.hooks.Emit(ctx, hooks.Event{Type: hooks.EventIterationStart, ...})` |
| `react.go` — all exit paths | `turn_done` | **`defer`** at top of function: `defer func() { s.hooks.Emit(ctx, hooks.Event{Type: hooks.EventTurnDone, ...}) }()` — covers all 3 return paths (normal answer, task recovery answer, iteration-limit fallback) |
| `tool_executor.go` | `before_tool_exec`, `after_tool_exec` | **Already emitted** — no changes needed. The accumulator handles these existing events. |
| `tasks.go` — `Complete()` | `task_completed` | Add accumulator field to `TaskTracker`. Emit in `Complete()`. |
| `tasks.go` — `Fail()` | `task_completed` | Same as above, emit in `Fail()` with error payload. |
| `tasks.go` — `Launch()` | `task_launched` | Emit after task is added to tracker. |

### TaskTracker accumulator wiring

`TaskTracker` gets a new optional field:

```go
type TaskTracker struct {
    // ... existing fields ...
    accumulator *EventAccumulator // optional, nil for sub-agent trackers
}
```

Set via a setter or constructor parameter. When non-nil, `Launch()`, `Complete()`, and `Fail()` emit events to the hooks bus. The accumulator reference is needed because `TaskTracker` doesn't currently have access to the `hooks.Bus` — but since the `EventAccumulator` is already a hook on the bus, we can either:

- (a) Give `TaskTracker` a reference to the `hooks.Bus` and emit events directly, or
- (b) Give `TaskTracker` a callback `func(hooks.Event)` set during wiring

Option (a) is simpler and consistent with how `ToolExecutor` already uses the bus:

```go
type TaskTracker struct {
    // ... existing fields ...
    hooks *hooks.Bus // optional, nil for sub-agent trackers
}

func (tt *TaskTracker) Complete(id int, result string) {
    tt.mu.Lock()
    // ... existing logic ...
    tt.mu.Unlock()
    tt.signalDone()

    if tt.hooks != nil {
        tt.hooks.Emit(context.Background(), hooks.Event{
            Type: hooks.EventTaskCompleted,
            Payload: map[string]any{
                "task_id":     id,
                "description": t.Description,
                "result":      stringutil.Truncate(result, 200),
            },
        })
    }
}
```

### Wiring

**Default session (app.go — BuildAgent):**

1. Create `EventAccumulator`, register on the `hooks.Bus`
2. For remote channels: create `ProgressReporter`, register on the same `hooks.Bus`
3. Pass accumulator into `Session` via `WithAccumulator` option
4. Pass `hooks.Bus` into `TaskTracker` (new field)
5. Register `report_progress` tool with `hooks.Bus` reference (remote channels only)

No new `MessageRouter` needed — `/status` already works through the existing `handleSlashCommand` in `app.go`.

**Managed sessions (SessionManager / SessionFactory):**

Per-chat sessions created by `SessionManager.createSession()` also need the accumulator and progress reporter. The `SessionFactory` closure (used by `SessionManager`) must be extended:

- The `SessionFactory` already receives the `hooks.Bus` (via `BuildDefaultBus`). Add the `channel.Channel` reference to the factory closure's captures.
- Inside the factory, create a new `EventAccumulator` and `ProgressReporter` per session, register both on the session's `hooks.Bus`.
- Register the `report_progress` tool in the per-session tool registry (the factory already creates per-session registries via the tool factory).

This ensures every managed session gets its own accumulator and reporter, scoped to its chat ID.

## Files Changed

| File | Type | Est. Lines |
|------|------|-----------|
| `internal/hooks/hooks.go` | Modified | ~10 (new EventType constants) |
| `internal/agent/accumulator.go` | New | ~120 |
| `internal/agent/progress_reporter.go` | New | ~50 |
| `internal/agent/progress_format.go` | New | ~60 (FormatProgress + helpers) |
| `internal/tools/builtin/progress_tool.go` | New | ~50 |
| `internal/agent/react.go` | Modified | ~8 (turn_start, iteration_start, defer turn_done) |
| `internal/agent/tasks.go` | Modified | ~15 (hooks.Bus field + emit in Launch/Complete/Fail) |
| `internal/agent/session.go` | Modified | ~10 (accumulator field + extended StatusInfo) |
| `internal/app/app.go` | Modified | ~12 (wire accumulator, reporter, progress tool) |

**Total:** ~4 new files (~280 lines), ~5 modified files (~55 lines). Minimal behavioral changes — the existing `/status` output is extended (not replaced), and existing hook events are consumed (not modified).

## Testing Strategy

- **Unit tests** for EventAccumulator: publish events via `Handle()`, verify `Snapshot()` contents, verify rolling window eviction, verify concurrent read/write safety with `go test -race`
- **Unit tests** for FormatProgress: table-driven tests for active/idle/with-tasks states
- **Unit tests** for ProgressReporter: verify throttling (send two events within minGap, assert only one `SendDirect` call), verify error logging on send failure
- **Unit tests** for report_progress tool: verify `phase_update` event is emitted on the bus
- **Integration test**: Wire accumulator + reporter on a real `hooks.Bus`, emit a sequence of events, verify snapshot and reporter output
- **Table-driven tests** with `t.Run` subtests per Go conventions
