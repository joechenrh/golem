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

### Factory Refactor

Current code has two factories in `buildToolFactories`:
- `base` — no spawn_agent (used by sub-agents, scheduler)
- `withSpawn` — adds spawn_agent (used by main/managed sessions)

Replace with a single recursive factory:

```go
func buildToolFactory(
    depth int,
    cfg *config.Config,
    exec, subExec executor.Executor, // subExec has RTK disabled
    filesystem *fs.LocalFS,
    larkCh *larkchan.LarkChannel,
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

        if depth > 0 {
            childFactory := buildToolFactory(depth-1, cfg, exec, subExec, ...)
            var seq atomic.Int64
            r.Register(builtin.NewSpawnAgentTool(func(ctx context.Context, prompt string) (string, error) {
                n := seq.Add(1)
                tapePath := filepath.Join(agentTapeDir, fmt.Sprintf("sub-%d-%s.jsonl", n, time.Now().Format("20060102-150405")))
                sess, err := buildEphemeralSession(llmClient, cfg, childFactory, logger, "sub-agent", tapePath, extHookRunner)
                if err != nil {
                    return "", fmt.Errorf("sub-agent: %w", err)
                }
                // Link child tracker to parent task for tree traversal
                tracker := tools.GetTaskTracker(ctx)
                // (SetChildTracker called inside Launch callback — see TaskTracker section)
                return sess.HandleInput(ctx, channel.IncomingMessage{
                    ChannelName: "internal",
                    Text:        prompt,
                })
            }))
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
func (tt *TaskTracker) SetChildTracker(taskID int, child *TaskTracker)

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

```go
func (tt *TaskTracker) TreeSummary(indent string) string {
    tt.mu.Lock()
    defer tt.mu.Unlock()

    var sb strings.Builder
    for _, task := range tt.tasks {
        status := task.Status.String()
        sb.WriteString(fmt.Sprintf("%s└─ #%d %s [%s]\n", indent, task.ID, task.Description, status))
        if task.ChildTracker != nil {
            sb.WriteString(task.ChildTracker.TreeSummary(indent + "   "))
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

## Change Summary

| Category | Change | Files |
|----------|--------|-------|
| Config | Add `MaxSpawnDepth int` (default 2) | `internal/config/config.go` |
| Factory | Replace `buildToolFactories` with recursive `buildToolFactory(depth)` | `internal/app/app.go` |
| TaskTracker | Add `ChildTracker` field to `BackgroundTask` | `internal/agent/tasks.go` |
| TaskTracker | Add `SetChildTracker`, `TreeSummary` methods | `internal/agent/tasks.go` |
| Session | Add `Tasks()` method to expose TaskTracker | `internal/agent/session.go` |
| Spawn runner | Call `SetChildTracker` after creating sub-session | `internal/app/app.go` |
| /status | Use `TreeSummary` in `StatusInfo()` | `internal/agent/session.go` |
| RTK | Keep `DisableRTK` for non-root depths via `subExec` | `internal/app/app.go` |
