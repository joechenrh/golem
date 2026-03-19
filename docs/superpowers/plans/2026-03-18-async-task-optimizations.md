# Async Task Optimizations Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Optimize the async task system — reduce token waste via head+tail truncation, eliminate post-completion LLM call cycles, add readable task names and duration to `/status`, and improve the fix-pr skill with phased execution and templates.

**Architecture:** Group A (tasks 1-4) are independent code changes to executor, react loop, spawn tool, and task tracker. Group B (task 5) is a single skill file update combining all prompt optimizations.

**Tech Stack:** Go 1.22+

**Spec:** `docs/superpowers/specs/2026-03-18-async-task-optimizations-design.md`

---

## File Structure

| Action | File | Responsibility |
|--------|------|---------------|
| Modify | `internal/stringutil/truncate.go` | Add `TruncateHeadTail` |
| Modify | `internal/stringutil/truncate_test.go` | Tests for head+tail truncation |
| Modify | `internal/executor/local.go` | Use `TruncateHeadTail` instead of `TruncateWithNote` |
| Modify | `internal/agent/react.go` | Move task wait to loop top with `lastWasTextOnly` flag |
| Modify | `internal/agent/agent_test.go` | Test for task wait optimization |
| Modify | `internal/tools/builtin/spawn_tool.go` | Add `description` parameter |
| Modify | `internal/tools/builtin/spawn_tool_test.go` | Test description param |
| Modify | `internal/agent/tasks.go` | Add timing to `TreeSummary` |
| Modify | `~/.golem/agents/lark-bot/skills/fix-pr/SKILL.md` | B1+B2+B3 skill updates |

---

## Chunk 1: Group A — Code Enforcements

### Task 1: Head+Tail Truncation (A1)

**Files:**
- Modify: `internal/stringutil/truncate.go`
- Create: `internal/stringutil/truncate_test.go` (if not exists, add tests)
- Modify: `internal/executor/local.go:64-66`

- [ ] **Step 1: Write failing tests**

Add to `internal/stringutil/truncate_test.go` (or `internal/executor/executor_test.go` if truncate tests are there — check `TestTruncate` location):

```go
func TestTruncateHeadTail(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxBytes int
		wantHead string // expected start
		wantTail string // expected end
		wantFull bool   // if true, output == input
	}{
		{
			name:     "short input unchanged",
			input:    "hello",
			maxBytes: 100,
			wantFull: true,
		},
		{
			name:     "exact limit unchanged",
			input:    strings.Repeat("x", 100),
			maxBytes: 100,
			wantFull: true,
		},
		{
			name:     "long input truncated with head and tail",
			input:    strings.Repeat("H", 500) + strings.Repeat("T", 500),
			maxBytes: 200,
			wantHead: "HHHH",
			wantTail: "TTTT",
		},
		{
			name:     "contains truncated note",
			input:    strings.Repeat("x", 1000),
			maxBytes: 200,
			wantHead: "xxx",
			wantTail: "xxx",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateHeadTail(tt.input, tt.maxBytes)
			if tt.wantFull {
				if got != tt.input {
					t.Errorf("expected unchanged input, got len=%d", len(got))
				}
				return
			}
			if len(got) > tt.maxBytes+50 { // allow small overflow for note
				t.Errorf("output too long: len=%d, maxBytes=%d", len(got), tt.maxBytes)
			}
			if !strings.HasPrefix(got, tt.wantHead) {
				t.Errorf("head mismatch: got prefix %q", got[:min(20, len(got))])
			}
			if !strings.HasSuffix(got, tt.wantTail) {
				t.Errorf("tail mismatch: got suffix %q", got[max(0, len(got)-20):])
			}
			if !strings.Contains(got, "[truncated") {
				t.Error("missing truncation note")
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/stringutil/ -run TestTruncateHeadTail -v`
Expected: FAIL — `TruncateHeadTail` not defined

- [ ] **Step 3: Implement TruncateHeadTail**

Add to `internal/stringutil/truncate.go`:

```go
// TruncateHeadTail returns s truncated to approximately maxBytes by keeping
// the head and tail, with a note in the middle showing how many bytes were
// removed. The total output (head + note + tail) fits within maxBytes.
// Falls back to TruncateWithNote if maxBytes is too small for the note.
func TruncateHeadTail(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	removed := len(s) - maxBytes
	note := fmt.Sprintf("\n... [truncated %d bytes] ...\n", removed)
	if len(note) >= maxBytes {
		return TruncateWithNote(s, maxBytes)
	}
	headBytes := (maxBytes - len(note)) / 2
	tailBytes := maxBytes - len(note) - headBytes
	return s[:headBytes] + note + s[len(s)-tailBytes:]
}
```

Add `"fmt"` to imports if not present.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/stringutil/ -run TestTruncateHeadTail -v`
Expected: PASS

- [ ] **Step 5: Replace TruncateWithNote in executor**

In `internal/executor/local.go`, lines 64-65, change:

```go
		Stdout:  stringutil.TruncateWithNote(stdout.String(), maxOutputBytes),
		Stderr:  stringutil.TruncateWithNote(stderr.String(), maxOutputBytes),
```

To:

```go
		Stdout:  stringutil.TruncateHeadTail(stdout.String(), maxOutputBytes),
		Stderr:  stringutil.TruncateHeadTail(stderr.String(), maxOutputBytes),
```

- [ ] **Step 6: Run full tests**

Run: `go build ./... && go vet ./... && go test ./internal/stringutil/ ./internal/executor/ -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/stringutil/truncate.go internal/executor/local.go
# Add test file too (wherever the test was added)
git -c commit.gpgsign=false commit -m "feat: head+tail truncation for shell output"
```

---

### Task 2: Fix Task Wait Post-Completion Cycle (A2)

**Files:**
- Modify: `internal/agent/react.go:82-200`

- [ ] **Step 1: Add `lastWasTextOnly` flag and move task wait**

In `internal/agent/react.go`, make these changes:

1. Add flag after line 85 (`emptyRetries := 0`):
```go
	lastWasTextOnly := false
```

2. After line 100 (`s.toolExec.InjectCompletedTasks()...`), add the proactive wait:
```go
		// If the LLM already gave a text-only response and background tasks
		// are still running, wait for them before the next LLM call. This
		// avoids the cycle: LLM text-only → task wait → inject → LLM text-only.
		if lastWasTextOnly && s.tasks.HasRunning() {
			s.logger.Debug("proactive task wait before LLM call", zap.Int("iter", iter))
			if stream && tokenCh != nil {
				tokenCh <- s.tasks.Summary()
			}
			s.tasks.WaitForAny(ctx)
			s.ephemeralMessages = append(s.ephemeralMessages, s.toolExec.InjectCompletedTasks()...)
			lastWasTextOnly = false
		}
```

3. In the tool calls branch (after line 120 `if len(resp.ToolCalls) > 0`), add reset:
```go
			lastWasTextOnly = false
```
(Add right before the existing `continue` at line 139.)

4. Remove the old task wait block (lines 187-200):
```go
		// Task wait: if background tasks are still running, send a status
		// summary to the user and wait in-memory for completion instead
		// of returning. This avoids burning iterations on polling.
		if s.tasks.HasRunning() {
			...
			continue
		}
```

Replace it with just setting the flag:
```go
		// Background tasks still running — mark for proactive wait on next iteration.
		if s.tasks.HasRunning() {
			s.logger.Debug("background tasks running, will wait on next iteration",
				zap.Int("iter", iter))
			if stream && tokenCh != nil {
				tokenCh <- s.tasks.Summary()
			}
			lastWasTextOnly = true
			s.ephemeralMessages = append(s.ephemeralMessages,
				llm.Message{Role: llm.RoleAssistant, Content: resp.Content},
			)
			continue
		}
```

This way:
- First time LLM gives text-only with running tasks → stream summary, set flag, continue
- Next iteration top → proactive wait, inject results, clear flag
- Next LLM call has all results → clean final answer → exits

- [ ] **Step 2: Verify the recovery section (lines 211+) still works**

The recovery section at the end of the loop (after `MaxToolIter` reached) also has task wait logic. This should NOT be changed — it's a different path (iteration limit reached). Verify it's untouched.

- [ ] **Step 3: Run tests**

Run: `go build ./... && go vet ./... && go test ./internal/agent/ -v -race`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add internal/agent/react.go
git -c commit.gpgsign=false commit -m "fix: move task wait before LLM call to avoid post-completion token waste"
```

---

### Task 3: Readable Task Names (A3)

**Files:**
- Modify: `internal/tools/builtin/spawn_tool.go`
- Modify: `internal/tools/builtin/spawn_tool_test.go`

- [ ] **Step 1: Write failing test**

Add to `internal/tools/builtin/spawn_tool_test.go`:

```go
func TestSpawnAgentTool_Description(t *testing.T) {
	creator := func(_ context.Context) (*SubAgentSession, error) {
		return &SubAgentSession{
			Tracker: &mockTracker{},
			Runner: func(_ context.Context, prompt string) (string, error) {
				return "done", nil
			},
		}, nil
	}
	tool := NewSpawnAgentTool(creator)

	tracker := &mockTracker{}
	ctx := tools.WithTaskTracker(context.Background(), tracker)

	args, _ := json.Marshal(map[string]string{
		"prompt":      "Fix the authentication bug in login.go by checking token expiry",
		"description": "fix auth bug",
	})
	result, err := tool.Execute(ctx, string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(result, "fix auth bug") {
		t.Errorf("result should contain description, got: %s", result)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools/builtin/ -run TestSpawnAgentTool_Description -v`
Expected: FAIL — description not in output

- [ ] **Step 3: Add description to params and Execute**

In `internal/tools/builtin/spawn_tool.go`:

1. Update `spawnAgentParams` — add `description` property:
```go
var spawnAgentParams = json.RawMessage(`{
	"type": "object",
	"properties": {
		"prompt": {
			"type": "string",
			"description": "The task description for the sub-agent. Be specific about what you need it to do and what output you expect."
		},
		"context": {
			"type": "string",
			"description": "Key context, files, or partial results to pass to the sub-agent. This is prepended to the prompt so the sub-agent has the information it needs without re-discovering it."
		},
		"description": {
			"type": "string",
			"description": "Short human-readable task name for status display, e.g. 'fix #67041'. If omitted, a truncated prompt is used."
		}
	},
	"required": ["prompt"]
}`)
```

2. Update the params struct in `Execute()` — add `Description`:
```go
	var params struct {
		Prompt      string `json:"prompt"`
		Context     string `json:"context"`
		Description string `json:"description"`
	}
```

3. Update the desc assignment (around the `tracker.Launch` call):
```go
	desc := params.Description
	if desc == "" {
		desc = truncateDesc(params.Prompt, 100)
	}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/tools/builtin/ -run TestSpawnAgentTool -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tools/builtin/spawn_tool.go internal/tools/builtin/spawn_tool_test.go
git -c commit.gpgsign=false commit -m "feat: add description parameter to spawn_agent for readable task names"
```

---

### Task 4: Duration in TreeSummary (A4)

**Files:**
- Modify: `internal/agent/tasks.go:299-324`
- Modify: `internal/agent/agent_test.go`

- [ ] **Step 1: Write failing test**

Add to `internal/agent/agent_test.go`:

```go
func TestTaskTracker_TreeSummary_WithDuration(t *testing.T) {
	tt := NewTaskTracker(5)

	// Add a running task
	tt.Add("running task", nil)

	// Add a completed task (manipulate times)
	id2 := tt.Add("done task", nil)
	tt.Complete(id2, "result")

	summary := tt.TreeSummary("")
	// Running task should show elapsed time
	if !strings.Contains(summary, "running task [running") {
		t.Errorf("missing running task with status: %s", summary)
	}
	// Completed task should show "completed in"
	if !strings.Contains(summary, "done task [completed in") {
		t.Errorf("missing completed task with duration: %s", summary)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestTaskTracker_TreeSummary_WithDuration -v`
Expected: FAIL — format doesn't match

- [ ] **Step 3: Update TreeSummary**

In `internal/agent/tasks.go`, update `TreeSummary` method (lines 299-324):

```go
func (tt *TaskTracker) TreeSummary(indent string) string {
	tt.mu.Lock()
	type entry struct {
		id           int
		desc         string
		status       TaskStatus
		startedAt    time.Time
		completedAt  time.Time
		childTracker tools.BackgroundTaskTracker
	}
	entries := make([]entry, 0, len(tt.tasks))
	for i := range tt.seq {
		id := i + 1
		if t, ok := tt.tasks[id]; ok {
			entries = append(entries, entry{id, t.Description, t.Status, t.StartedAt, t.CompletedAt, t.ChildTracker})
		}
	}
	tt.mu.Unlock()

	var sb strings.Builder
	for _, e := range entries {
		var durStr string
		switch e.status {
		case TaskRunning:
			durStr = fmt.Sprintf(" %s", time.Since(e.startedAt).Truncate(time.Second))
		case TaskCompleted:
			durStr = fmt.Sprintf(" in %s", e.completedAt.Sub(e.startedAt).Truncate(time.Second))
		case TaskFailed:
			durStr = fmt.Sprintf(" in %s", e.completedAt.Sub(e.startedAt).Truncate(time.Second))
		}
		fmt.Fprintf(&sb, "%s|- #%d %s [%s%s]\n", indent, e.id, e.desc, e.status, durStr)
		if e.childTracker != nil {
			sb.WriteString(e.childTracker.TreeSummary(indent + "   "))
		}
	}
	return sb.String()
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/agent/ -run TestTaskTracker_TreeSummary -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/agent/tasks.go internal/agent/agent_test.go
git -c commit.gpgsign=false commit -m "feat: show duration in /status tree display"
```

---

## Chunk 2: Group B — Skill Optimizations

### Task 5: Update fix-pr skill (B1+B2+B3)

**Files:**
- Modify: `~/.golem/agents/lark-bot/skills/fix-pr/SKILL.md`

- [ ] **Step 1: Add phased execution to sub-agent section (B1)**

In the skill file's "For Sub-Agent" section, after the "CRITICAL RULES" block, add:

```markdown
### Phased Execution (MANDATORY)

You MUST delegate work to sub-sub-agents using `spawn_agent`. Do NOT do analysis and fixing in the same session — this wastes context.

**Phase 1 — Analyze:** Spawn a sub-agent with `description: "analyze #<issue_number>"` to:
- Read the issue via `gh issue view`
- Search the codebase for relevant code
- Reproduce the issue using the running TiDB at 127.0.0.1:4000
- Return ONLY: root cause location (file:line), one-sentence explanation, suggested fix approach
- Target output: <500 tokens

**Phase 2 — Fix:** After analysis returns, spawn another sub-agent with `description: "fix #<issue_number>"` and pass the analysis result as `context`. This agent:
- Writes the code fix
- Writes/updates tests
- Runs tests
- Creates the PR using the script template below

Each spawn_agent call MUST include a `description` parameter for readable /status display.
```

- [ ] **Step 2: Add Git/PR script template (B2)**

Add to the skill file, in the sub-agent workflow section (replace the current step 5 "Merge" or add as a new section):

```markdown
### Git/PR Script Template

When ready to commit and create a PR, run this as a single shell command (fill in placeholders):

\```bash
set -e
cd <repo_dir>
git add -A
git commit -m "<commit_msg>"
if ! git remote | grep -q '^fork$'; then
  git remote add fork https://github.com/<user>/<repo>.git
fi
git push -u fork <branch>
gh pr create --repo <upstream_owner>/<upstream_repo> --head <user>:<branch> --base master \
  --title "<title>" --body "<body>"
\```

This saves multiple iterations. Run it in ONE shell call, not step by step.
```

- [ ] **Step 3: Add TiDB reuse instruction (B3)**

Add to the skill file, in the "TiDB Connection" section or near the reproduction steps:

```markdown
### Reproducing SQL Issues

To reproduce SQL issues, connect to the running TiDB instance using the mysql CLI:
\```bash
mysql -h 127.0.0.1 -P 4000 -u root test -e "<SQL>"
\```

Do NOT compile TiDB from source for reproduction. A TiDB instance is already running.
```

- [ ] **Step 4: Add MaxSpawnDepth prerequisite note**

Add near the top of the skill file:

```markdown
**Prerequisite:** `MaxSpawnDepth` must be >= 2 (the default) for phased execution to work.
```

- [ ] **Step 5: Verify skill file is well-formed**

```bash
ls -la ~/.golem/agents/lark-bot/skills/fix-pr/SKILL.md
head -5 ~/.golem/agents/lark-bot/skills/fix-pr/SKILL.md
```

Verify frontmatter is intact (name, description).

---

## Summary

| Task | Description | Steps |
|------|-------------|-------|
| 1 | Head+tail truncation (A1) | 7 |
| 2 | Task wait optimization (A2) | 4 |
| 3 | Readable task names (A3) | 5 |
| 4 | Duration in TreeSummary (A4) | 5 |
| 5 | Skill updates (B1+B2+B3) | 5 |
