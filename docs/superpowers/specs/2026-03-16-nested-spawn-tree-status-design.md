# Nested Spawn + Tree Status Design

## Overview

Allow sub-agents to spawn their own sub-agents (one-level nesting by default, configurable depth), and display the task hierarchy as a tree in `/status`.

### Motivation

Long-running tasks like fix-pr require multiple phases (analyze, code, test, PR). A single sub-agent exhausts its context on analysis, leaving no room for implementation. Nested spawn lets a sub-agent delegate sub-tasks — e.g., an "analysis" sub-sub-agent returns a concise summary, freeing the parent sub-agent's context for the next phase.

### Design Principles

- **Configurable depth** — `MaxSpawnDepth` in config (default 2). Main session spawns at depth N, child at N-1, until depth 0 (no spawn).
- **Per-session TaskTracker** — each session owns its tracker. Parent trackers hold references to children for tree traversal.
- **Minimal changes** — replace `base`/`withSpawn` dual factory with a single recursive `buildToolFactory(depth)`.

## Depth Control

### Config

New field in `config.Config`:

```go
MaxSpawnDepth int // default 2; 0 disables spawn entirely
```

Loaded from env var `GOLEM_MAX_SPAWN_DEPTH` in `Load()`, with `strconv.Atoi` and default 2. Validation: must be >= 0.

### Factory Refactor

Current code has two factories in `buildToolFactories`:
- `base` — no spawn_agent (used by sub-agents, scheduler)
- `withSpawn` — adds spawn_agent (used by main/managed sessions)

Replace with a single recursive factory. Simplified structure (see Wiring section for the canonical spawn runner implementation):

```go
func buildToolFactory(
    depth int,
    cfg *config.Config,
    exec, subExec executor.Executor, // subExec has RTK disabled
    filesystem *fs.LocalFS,
    larkCh *larkchan.LarkChannel,
    schedStore *scheduler.Store,
    llmClient llm.Client,
    agentTapeDir string,
    extHookRunner agent.ExtHookRunner,
    logger *zap.Logger,
) func() *tools.Registry {
    return func() *tools.Registry {
        // Use subExec (no RTK) for non-root depth
        e := exec
        if depth < cfg.MaxSpawnDepth {
            e = subExec
        }
        r := BuildToolRegistry(cfg, e, filesystem, larkCh, logger)

        // Schedule tools only for root session
        if depth == cfg.MaxSpawnDepth && schedStore != nil {
            r.RegisterAll(
                builtin.NewScheduleAddTool(schedStore, nil),
                builtin.NewScheduleListTool(schedStore),
                builtin.NewScheduleRemoveTool(schedStore, nil),
            )
            r.Expand("schedule_add")
            r.Expand("schedule_list")
            r.Expand("schedule_remove")
        }

        if depth > 0 {
            childFactory := buildToolFactory(depth-1, cfg, exec, subExec, ...)
            // Register spawn_agent with runner — see Wiring section for full implementation
            r.Register(builtin.NewSpawnAgentTool(spawnRunner(childFactory, ...)))
        }

        return r
    }
}
```

Call sites:
- Main/managed sessions: `buildToolFactory(cfg.MaxSpawnDepth, ...)`
- Scheduler: `buildToolFactory(0, ...)` (no spawn, same as before)

### RTK Handling

Sub-agents (depth < MaxSpawnDepth) use `subExec` with `DisableRTK=true` to avoid shell command rewriting issues with multi-command chains. The root session (depth == MaxSpawnDepth) uses the normal executor with RTK enabled.

## TaskTracker Tree

### BackgroundTask Changes

```go
type BackgroundTask struct {
    ID           int
    Description  string
    Status       TaskStatus
    Result       string
    ChildTracker *TaskTracker // sub-agent's tracker; nil if no children
}
```

### New Methods

```go
// SetChildTracker links a child session's TaskTracker to a parent task.
// Called after the sub-agent session is created, before HandleInput.
// Must acquire tt.mu since it's called from a Launch goroutine while
// TreeSummary may be reading concurrently.
func (tt *TaskTracker) SetChildTracker(taskID int, child *TaskTracker) {
    tt.mu.Lock()
    defer tt.mu.Unlock()
    if t, ok := tt.tasks[taskID]; ok {
        t.ChildTracker = child
    }
}

// TreeSummary returns a tree-formatted string of all tasks and their children.
func (tt *TaskTracker) TreeSummary(indent string) string
```

### Wiring

In the spawn_agent runner, after creating the ephemeral session:

```go
taskID := tracker.Launch(desc, func(taskCtx context.Context, id int) {
    sess, err := buildEphemeralSession(...)
    if err != nil {
        tracker.Fail(id, err.Error())
        return
    }
    tracker.SetChildTracker(id, sess.Tasks())
    result, err := sess.HandleInput(taskCtx, ...)
    if err != nil {
        tracker.Fail(id, err.Error())
    } else {
        tracker.Complete(id, result)
    }
})
```

This requires `Session` to expose its `TaskTracker` via a `Tasks()` method. The tracker is set before `HandleInput` runs, so it's available for tree queries while the sub-agent is active.

### TreeSummary Implementation

Uses copy-then-recurse to avoid holding the mutex while recursing into child trackers (prevents latency issues and potential lock-ordering problems). Iterates by sequential ID for deterministic output, matching the existing `Summary()` pattern.

```go
func (tt *TaskTracker) TreeSummary(indent string) string {
    // Snapshot under lock, then release before recursing into children.
    tt.mu.Lock()
    type entry struct {
        id           int
        desc         string
        status       string
        childTracker *TaskTracker
    }
    entries := make([]entry, 0, len(tt.tasks))
    for i := range tt.seq {
        id := i + 1
        if t, ok := tt.tasks[id]; ok {
            entries = append(entries, entry{id, t.Description, t.Status.String(), t.ChildTracker})
        }
    }
    tt.mu.Unlock()

    var sb strings.Builder
    for _, e := range entries {
        sb.WriteString(fmt.Sprintf("%s|- #%d %s [%s]\n", indent, e.id, e.desc, e.status))
        if e.childTracker != nil {
            sb.WriteString(e.childTracker.TreeSummary(indent + "   "))
        }
    }
    return sb.String()
}
```

## /status Display

`StatusInfo()` in `session.go` currently shows `Background tasks: N running`. Replace with tree output:

```
Model: codex:gpt-5.4
Tools: 27
Tokens used: 110750 (prompt: 109566, completion: 1184)

Background tasks:
└─ #1 fix issue #67018 [running]
   └─ #1 analyze root cause [running]
   └─ #2 write fix [pending]
```

When no background tasks exist, show nothing (same as current behavior).

Task IDs are local to each TaskTracker (each level has independent numbering). Tree indentation uses 3 spaces per level.

## Notes

- **`Summary()` method**: The existing `Summary()` (used in the ReAct loop to tell the LLM about task status) stays flat — only `TreeSummary` is hierarchical. The LLM only needs to know about its own direct children.
- **`FullDescription()` update**: `SpawnAgentTool.FullDescription()` currently says "cannot spawn further agents." Update to reflect depth-aware behavior: "Sub-agents can spawn further sub-agents up to the configured depth limit."
- **Cleanup**: Context cancellation propagates through `context.WithCancel` in `TaskTracker.Launch`. When a parent task's context is cancelled, child sessions' contexts are also cancelled, which cancels their tasks recursively. No additional cleanup code needed.

## Change Summary

| Category | Change | Files |
|----------|--------|-------|
| Config | Add `MaxSpawnDepth int` (default 2), env `GOLEM_MAX_SPAWN_DEPTH` | `internal/config/config.go` |
| Factory | Replace `buildToolFactories` with recursive `buildToolFactory(depth)` | `internal/app/app.go` |
| TaskTracker | Add `ChildTracker` field to `BackgroundTask` | `internal/agent/tasks.go` |
| TaskTracker | Add `SetChildTracker`, `TreeSummary` methods | `internal/agent/tasks.go` |
| Session | Add `Tasks()` method to expose TaskTracker (if not already present) | `internal/agent/session.go` |
| Spawn runner | Call `SetChildTracker` after creating sub-session | `internal/app/app.go` |
| Spawn tool | Update `FullDescription()` to reflect depth-aware nesting | `internal/tools/builtin/spawn_tool.go` |
| /status | Use `TreeSummary` in `StatusInfo()` | `internal/agent/session.go` |
| RTK | Keep `DisableRTK` for non-root depths via `subExec` | `internal/app/app.go` |
| Design doc | Update design/06-tools.md to remove "sub-agents cannot spawn" | `design/06-tools.md` |
