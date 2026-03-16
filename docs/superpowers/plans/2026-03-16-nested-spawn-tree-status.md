# Nested Spawn + Tree Status Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow sub-agents to spawn their own sub-agents (configurable depth) and display the task hierarchy as a tree in `/status`.

**Architecture:** Replace the dual `base`/`withSpawn` factory with a single recursive `buildToolFactory(depth)`. Each session's TaskTracker gains a `ChildTracker` field for tree traversal. `/status` renders the tree via `TreeSummary()`.

**Tech Stack:** Go 1.22+

**Spec:** `docs/superpowers/specs/2026-03-16-nested-spawn-tree-status-design.md`

---

## File Structure

| Action | File | Responsibility |
|--------|------|---------------|
| Modify | `internal/config/config.go` | Add `MaxSpawnDepth` field |
| Modify | `internal/config/config_test.go` | Test for new config field |
| Modify | `internal/agent/tasks.go` | Add `ChildTracker`, `SetChildTracker`, `TreeSummary` |
| Modify | `internal/agent/agent_test.go` | Tests for tree methods |
| Modify | `internal/agent/session.go` | Update `StatusInfo()` to use `TreeSummary` |
| Modify | `internal/app/app.go` | Replace `buildToolFactories` with `buildToolFactory(depth)` |
| Modify | `internal/tools/builtin/spawn_tool.go` | Update `FullDescription()` |

---

## Chunk 1: Config + TaskTracker

### Task 1: Add MaxSpawnDepth to config

**Files:**
- Modify: `internal/config/config.go:101-108,198-213,261-279`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/config/config_test.go`:

```go
func TestMaxSpawnDepthDefault(t *testing.T) {
	t.Setenv("GOLEM_MODEL", "openai:test")
	cfg, err := Load("", nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxSpawnDepth != 2 {
		t.Errorf("MaxSpawnDepth = %d, want 2", cfg.MaxSpawnDepth)
	}
}

func TestMaxSpawnDepthFromEnv(t *testing.T) {
	t.Setenv("GOLEM_MODEL", "openai:test")
	t.Setenv("GOLEM_MAX_SPAWN_DEPTH", "3")
	cfg, err := Load("", nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxSpawnDepth != 3 {
		t.Errorf("MaxSpawnDepth = %d, want 3", cfg.MaxSpawnDepth)
	}
}

func TestMaxSpawnDepthValidation(t *testing.T) {
	t.Setenv("GOLEM_MODEL", "openai:test")
	t.Setenv("GOLEM_MAX_SPAWN_DEPTH", "-1")
	_, err := Load("", nil)
	if err == nil {
		t.Error("expected validation error for negative MaxSpawnDepth")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/ -run TestMaxSpawnDepth -v`
Expected: FAIL — `MaxSpawnDepth` field not found

- [ ] **Step 3: Add the config field**

In `internal/config/config.go`:

1. Add field after `MaxOutputTokens` (around line 102):
```go
	MaxSpawnDepth      int      // max sub-agent nesting depth (default: 2; 0 disables spawn)
```

2. Add loading in `Load()` (after `MaxOutputTokens` line, around line 199):
```go
		MaxSpawnDepth:       a.integer("GOLEM_MAX_SPAWN_DEPTH", 2),
```

3. Add validation in `validate()` (after `MaxToolIter` check, around line 267):
```go
	if c.MaxSpawnDepth < 0 {
		return fmt.Errorf("max spawn depth must be non-negative, got %d", c.MaxSpawnDepth)
	}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/config/ -run TestMaxSpawnDepth -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git -c commit.gpgsign=false commit -m "feat: add MaxSpawnDepth config (default 2)"
```

---

### Task 2: Add ChildTracker, SetChildTracker, TreeSummary to TaskTracker + interface

**Files:**
- Modify: `internal/tools/tasktracker.go` (add SetChildTracker to interface)
- Modify: `internal/agent/tasks.go`
- Modify: `internal/agent/agent_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/agent/agent_test.go`:

```go
func TestTaskTracker_SetChildTracker(t *testing.T) {
	parent := NewTaskTracker(5)
	child := NewTaskTracker(5)

	id := parent.Launch("parent task", func(ctx context.Context, taskID int) {
		// simulate work
	})
	parent.SetChildTracker(id, child)

	// Verify child is linked
	parent.Close()
	summary := parent.TreeSummary("")
	if !strings.Contains(summary, "parent task") {
		t.Errorf("TreeSummary missing parent task, got: %s", summary)
	}
}

func TestTaskTracker_TreeSummary(t *testing.T) {
	parent := NewTaskTracker(5)
	child := NewTaskTracker(5)

	pid := parent.Add("fix issue", nil)
	parent.SetChildTracker(pid, child)

	child.Add("analyze", nil)
	child.Add("write fix", nil)

	summary := parent.TreeSummary("")
	if !strings.Contains(summary, "fix issue") {
		t.Errorf("missing parent in tree: %s", summary)
	}
	if !strings.Contains(summary, "analyze") {
		t.Errorf("missing child 'analyze' in tree: %s", summary)
	}
	if !strings.Contains(summary, "write fix") {
		t.Errorf("missing child 'write fix' in tree: %s", summary)
	}
}

func TestTaskTracker_TreeSummary_Empty(t *testing.T) {
	tt := NewTaskTracker(5)
	if got := tt.TreeSummary(""); got != "" {
		t.Errorf("TreeSummary on empty tracker = %q, want empty", got)
	}
}

func TestTaskTracker_TreeSummary_NoChildren(t *testing.T) {
	tt := NewTaskTracker(5)
	tt.Add("solo task", nil)
	summary := tt.TreeSummary("")
	if !strings.Contains(summary, "solo task") {
		t.Errorf("missing task in summary: %s", summary)
	}
	if strings.Contains(summary, "   ") {
		// Should not have child indentation
		t.Errorf("unexpected child indentation: %s", summary)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/ -run TestTaskTracker_SetChildTracker -v && go test ./internal/agent/ -run TestTaskTracker_TreeSummary -v`
Expected: FAIL — methods not defined

- [ ] **Step 3: Add ChildTracker field to BackgroundTask**

In `internal/agent/tasks.go`, add to `BackgroundTask` struct (after `Error` field, line 47):

```go
	ChildTracker *TaskTracker // sub-agent's tracker; nil if no children
```

- [ ] **Step 4: Add SetChildTracker to BackgroundTaskTracker interface**

In `internal/tools/tasktracker.go`, add to the interface:

```go
type BackgroundTaskTracker interface {
	Launch(desc string, fn func(ctx context.Context, id int)) int
	Complete(id int, result string)
	Fail(id int, errMsg string)
	// SetChildTracker links a child TaskTracker to a task for tree display.
	SetChildTracker(id int, child BackgroundTaskTracker)
}
```

- [ ] **Step 5: Add SetChildTracker method to TaskTracker**

Add after the `Complete` method in `internal/agent/tasks.go` (around line 141):

```go
// SetChildTracker links a child session's TaskTracker to a parent task.
func (tt *TaskTracker) SetChildTracker(taskID int, child BackgroundTaskTracker) {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	if t, ok := tt.tasks[taskID]; ok {
		t.ChildTracker = child
	}
}
```

Also update `BackgroundTask` struct — `ChildTracker` type should be `BackgroundTaskTracker` (interface) not `*TaskTracker`:

```go
	ChildTracker BackgroundTaskTracker // sub-agent's tracker; nil if no children
```

- [ ] **Step 6: Add TreeSummary method**

Add `TreeSummary` to the `BackgroundTaskTracker` interface in `internal/tools/tasktracker.go`:

```go
	// TreeSummary returns a tree-formatted string for /status display.
	TreeSummary(indent string) string
```

Add implementation after `Summary` method in `internal/agent/tasks.go` (around line 266):

```go
// TreeSummary returns a tree-formatted string of all tasks and their children.
// Uses copy-then-recurse to avoid holding the mutex while recursing into
// child trackers.
func (tt *TaskTracker) TreeSummary(indent string) string {
	tt.mu.Lock()
	type entry struct {
		id           int
		desc         string
		status       string
		childTracker BackgroundTaskTracker
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

Note: `BackgroundTaskTracker` is in the `tools` package. Import it in `tasks.go`:
```go
import "github.com/joechenrh/golem/internal/tools"
```
And use `tools.BackgroundTaskTracker` for the `ChildTracker` field type. **However**, this creates a circular import (`agent` → `tools` → `agent`). Check if `tools` already imports `agent` — if not, this is fine. If it does, use a local interface or keep `ChildTracker` as `any` with a type assertion in `TreeSummary`.

Actually, since `BackgroundTaskTracker` is defined in `tools` and `TaskTracker` is in `agent`, and `agent` already uses types from `tools` (via `tools.GetTaskTracker`), this import direction (`agent` → `tools`) should be safe. Verify with `go build`.

- [ ] **Step 7: Run tests**

Run: `go test ./internal/agent/ -run TestTaskTracker -v`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/tools/tasktracker.go internal/agent/tasks.go internal/agent/agent_test.go
git -c commit.gpgsign=false commit -m "feat: add ChildTracker + TreeSummary to TaskTracker"
```

---

## Chunk 2: Factory Refactor + Status

### Task 3: Replace buildToolFactories with recursive buildToolFactory

**Files:**
- Modify: `internal/app/app.go:404-510`
- Modify: `internal/tools/builtin/spawn_tool.go`

**Architecture note:** The spawn runner (SubAgentRunner) stays as a simple `func(ctx, prompt) -> (string, error)`. The async dispatch logic (Launch/Complete/Fail) stays in `spawn_tool.go`'s `Execute()`. We add a `SetChildTracker` call inside `Execute()`'s `Launch` callback. The runner just creates the session and runs HandleInput.

- [ ] **Step 1: Modify spawn_tool.go to call SetChildTracker**

In `internal/tools/builtin/spawn_tool.go`:

1. Change `SubAgentRunner` to return a session-like object that exposes a TaskTracker:

```go
// SubAgentSession is returned by the SessionCreator for tree tracking.
type SubAgentSession struct {
	Tracker tools.BackgroundTaskTracker // child session's tracker
	Runner  func(ctx context.Context, prompt string) (string, error)
}

// SessionCreator creates a sub-agent session ready to run.
type SessionCreator func(ctx context.Context) (*SubAgentSession, error)
```

2. Change `SpawnAgentTool` to use `SessionCreator`:

```go
type SpawnAgentTool struct {
	creator SessionCreator
}

func NewSpawnAgentTool(creator SessionCreator) *SpawnAgentTool {
	return &SpawnAgentTool{creator: creator}
}
```

3. Update `Execute()` to create session first, then wire child tracker:

```go
func (t *SpawnAgentTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Prompt  string `json:"prompt"`
		Context string `json:"context"`
	}
	if errMsg := tools.ParseArgs(args, &params); errMsg != "" {
		return errMsg, nil
	}
	if params.Prompt == "" {
		return "Error: 'prompt' is required", nil
	}

	prompt := params.Prompt
	if params.Context != "" {
		prompt = "[Context from parent agent]\n" + params.Context + "\n\n[Task]\n" + params.Prompt
	}

	sub, err := t.creator(ctx)
	if err != nil {
		return "Error creating sub-agent: " + err.Error(), nil
	}

	tracker := tools.GetTaskTracker(ctx)
	if tracker == nil {
		// Sync fallback for sub-sessions (which have no tracker).
		result, err := sub.Runner(ctx, prompt)
		if err != nil {
			return "Sub-agent error: " + err.Error(), nil
		}
		return result, nil
	}

	desc := truncateDesc(params.Prompt, 100)
	capturedPrompt := prompt
	taskID := tracker.Launch(desc, func(taskCtx context.Context, id int) {
		if sub.Tracker != nil {
			tracker.SetChildTracker(id, sub.Tracker)
		}
		result, err := sub.Runner(taskCtx, capturedPrompt)
		if err != nil {
			tracker.Fail(id, err.Error())
		} else {
			tracker.Complete(id, result)
		}
	})

	return fmt.Sprintf("Task #%d started: %s\nResults will be delivered automatically when the sub-agent finishes.", taskID, desc), nil
}
```

4. Update `FullDescription()`:
```go
"The sub-agent has its own conversation context and access to standard tools " +
"(shell, file I/O, web). Sub-agents can spawn further sub-agents up to the " +
"configured depth limit.\n\n" +
```

- [ ] **Step 2: Replace buildToolFactories in app.go**

Replace `buildToolFactories` (lines 463-510) with:

```go
// buildToolFactory creates a tool registry factory at the given spawn depth.
func buildToolFactory(
	depth int,
	cfg *config.Config,
	exec, subExec executor.Executor,
	filesystem *fs.LocalFS,
	larkCh *larkchan.LarkChannel,
	schedStore *scheduler.Store,
	llmClient llm.Client,
	agentTapeDir string,
	extHookRunner agent.ExtHookRunner,
	logger *zap.Logger,
) func() *tools.Registry {
	return func() *tools.Registry {
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
			childFactory := buildToolFactory(
				depth-1, cfg, exec, subExec, filesystem, larkCh,
				schedStore, llmClient, agentTapeDir, extHookRunner, logger,
			)
			var subAgentSeq atomic.Int64
			r.Register(builtin.NewSpawnAgentTool(func(ctx context.Context) (*builtin.SubAgentSession, error) {
				seq := subAgentSeq.Add(1)
				subTapePath := filepath.Join(agentTapeDir, fmt.Sprintf("sub-%d-%s.jsonl", seq, time.Now().Format("20060102-150405")))
				sess, err := buildEphemeralSession(llmClient, cfg, childFactory, logger, "sub-agent", subTapePath, extHookRunner)
				if err != nil {
					return nil, fmt.Errorf("sub-agent: %w", err)
				}
				return &builtin.SubAgentSession{
					Tracker: sess.Tasks(),
					Runner: func(runCtx context.Context, prompt string) (string, error) {
						return sess.HandleInput(runCtx, channel.IncomingMessage{
							ChannelName: "internal",
							Text:        prompt,
						})
					},
				}, nil
			}))
		}

		return r
	}
}
```

Note: No `truncateDesc` duplication needed — it stays in `spawn_tool.go`.

- [ ] **Step 3: Update the call site**

Change line 404 from:
```go
	toolFactory, spawnToolFactory := buildToolFactories(
		cfg, exec, filesystem, larkCh, schedStore,
		llmClient, agentTapeDir, extHookRunner, logger,
	)
```

To:
```go
	subExec := executor.NewLocal(cfg.WorkspaceDir)
	subExec.DisableRTK = true
	spawnToolFactory := buildToolFactory(
		cfg.MaxSpawnDepth, cfg, exec, subExec, filesystem, larkCh,
		schedStore, llmClient, agentTapeDir, extHookRunner, logger,
	)
	toolFactory := buildToolFactory(
		0, cfg, exec, subExec, filesystem, larkCh,
		schedStore, llmClient, agentTapeDir, extHookRunner, logger,
	)
```

Note: `exec` from `buildExecutorAndFS` returns `executor.Executor` (interface). `subExec` is `*executor.LocalExecutor` which implements `executor.Executor`. Both parameter types in `buildToolFactory` are `executor.Executor`.

- [ ] **Step 5: Build and test**

Run: `go build ./... && go vet ./... && go test ./... -race`
Expected: PASS (all 691+ tests)

- [ ] **Step 6: Commit**

```bash
git add internal/app/app.go internal/tools/builtin/spawn_tool.go
git -c commit.gpgsign=false commit -m "feat: replace dual factory with recursive buildToolFactory(depth)"
```

---

### Task 4: Update StatusInfo to use TreeSummary

**Files:**
- Modify: `internal/agent/session.go:450-470`

- [ ] **Step 1: Update StatusInfo**

In `internal/agent/session.go`, modify `StatusInfo()` (line 450). After the existing progress section, add tree display. Replace the function:

```go
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
			base += "\n\n" + FormatProgress(snap)
		}
	}

	if s.tasks != nil {
		tree := s.tasks.TreeSummary("")
		if tree != "" {
			base += "\n\nBackground tasks:\n" + tree
		}
	}

	return base
}
```

- [ ] **Step 2: Build and test**

Run: `go build ./... && go vet ./... && go test ./... -race`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add internal/agent/session.go
git -c commit.gpgsign=false commit -m "feat: show task tree in /status"
```

---

## Chunk 3: Cleanup

### Task 5: Update design docs

**Files:**
- Modify: `design/06-tools.md`

- [ ] **Step 1: Update design doc**

In `design/06-tools.md`, find the section about sub-agents/spawn_agent that says "sub-agents cannot spawn further agents" and update it to reflect the configurable depth behavior.

- [ ] **Step 2: Commit**

```bash
git add design/06-tools.md
git -c commit.gpgsign=false commit -m "docs: update design doc for nested spawn support"
```

---

## Summary

| Task | Description | Steps |
|------|-------------|-------|
| 1 | `MaxSpawnDepth` config | 5 |
| 2 | `ChildTracker` + `TreeSummary` on TaskTracker + interface | 8 |
| 3 | Recursive `buildToolFactory(depth)` + spawn wiring | 5 |
| 4 | `/status` tree display | 3 |
| 5 | Design doc update | 2 |
