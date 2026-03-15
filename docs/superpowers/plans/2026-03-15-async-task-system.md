# Async Task System Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enable the Lark bot to accept long-running tasks (e.g., "fix issue #123"), execute them asynchronously via sub-agents with TiDB persistence, and report results back.

**Architecture:** Skill-driven — a `fix_pr` skill orchestrates the workflow for both the main agent and sub-agent. One new internal tool (`lark_message`) gives sub-agents the ability to message Lark chats. A bug fix in `app.go` allows `/status` to work while sub-agents run.

**Tech Stack:** Go 1.22+, TiDB (MySQL-compatible), Lark API, `gh` CLI

**Spec:** `docs/superpowers/specs/2026-03-14-async-task-skill-design.md`

---

## File Structure

| Action | File | Responsibility |
|--------|------|---------------|
| Create | `internal/tools/builtin/lark_message_tool.go` | `lark_message` tool — thin wrapper around `SendDirect` |
| Create | `internal/tools/builtin/lark_message_tool_test.go` | Unit tests for lark_message tool |
| Modify | `internal/app/app.go:797-810` | Register `lark_message` in `BuildToolRegistry` |
| Modify | `internal/app/app.go:158-182` | Intercept slash commands before per-chat queue |
| Create | `internal/app/app_slash_test.go` | Tests for slash command interception |
| Create | `~/.golem/agents/lark-bot/skills/fix-pr/SKILL.md` | Skill file with main + sub-agent sections |

---

## Chunk 1: lark_message Tool

### Task 1: Create lark_message tool

**Files:**
- Create: `internal/tools/builtin/lark_message_tool.go`
- Create: `internal/tools/builtin/lark_message_tool_test.go`
- Modify: `internal/app/app.go:797-810`

- [ ] **Step 1: Write the failing test**

Create `internal/tools/builtin/lark_message_tool_test.go`:

```go
package builtin

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// mockDirectSender records SendDirect calls for testing.
type mockDirectSender struct {
	calls []struct {
		channelID string
		text      string
	}
	err error
}

func (m *mockDirectSender) SendDirect(_ context.Context, channelID, text string) error {
	m.calls = append(m.calls, struct {
		channelID string
		text      string
	}{channelID, text})
	return m.err
}

func TestLarkMessageTool_Execute(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		wantMsg string
		wantErr bool
	}{
		{
			name:    "success",
			args:    `{"channel_id": "oc_123", "text": "hello"}`,
			wantMsg: "Message sent to oc_123",
		},
		{
			name:    "missing channel_id",
			args:    `{"text": "hello"}`,
			wantMsg: "Error: 'channel_id' is required",
		},
		{
			name:    "missing text",
			args:    `{"channel_id": "oc_123"}`,
			wantMsg: "Error: 'text' is required",
		},
		{
			name:    "empty args",
			args:    `{}`,
			wantMsg: "Error: 'channel_id' is required",
		},
		{
			name:    "malformed JSON",
			args:    `not json`,
			wantMsg: "Error: invalid arguments",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sender := &mockDirectSender{}
			tool := NewLarkMessageTool(sender)

			result, err := tool.Execute(context.Background(), tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Execute() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.name == "malformed JSON" {
				if !strings.HasPrefix(result, tt.wantMsg) {
					t.Errorf("Execute() = %q, want prefix %q", result, tt.wantMsg)
				}
			} else if result != tt.wantMsg {
				t.Errorf("Execute() = %q, want %q", result, tt.wantMsg)
			}
		})
	}
}

func TestLarkMessageTool_Execute_SendError(t *testing.T) {
	sender := &mockDirectSender{err: errors.New("connection refused")}
	tool := NewLarkMessageTool(sender)

	result, err := tool.Execute(context.Background(), `{"channel_id": "oc_123", "text": "hello"}`)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	want := "Error sending message: connection refused"
	if result != want {
		t.Errorf("Execute() = %q, want %q", result, want)
	}
}

func TestLarkMessageTool_Execute_SendsCorrectMessage(t *testing.T) {
	sender := &mockDirectSender{}
	tool := NewLarkMessageTool(sender)

	tool.Execute(context.Background(), `{"channel_id": "oc_abc", "text": "test msg"}`)

	if len(sender.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(sender.calls))
	}
	if sender.calls[0].channelID != "oc_abc" {
		t.Errorf("channelID = %q, want %q", sender.calls[0].channelID, "oc_abc")
	}
	if sender.calls[0].text != "test msg" {
		t.Errorf("text = %q, want %q", sender.calls[0].text, "test msg")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools/builtin/ -run TestLarkMessageTool -v`
Expected: FAIL — `NewLarkMessageTool` not defined

- [ ] **Step 3: Write the implementation**

Create `internal/tools/builtin/lark_message_tool.go`:

```go
package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/joechenrh/golem/internal/tools"
)

// DirectSender sends a message to a specific channel. Satisfied by
// *larkchan.LarkChannel and test mocks.
type DirectSender interface {
	SendDirect(ctx context.Context, channelID, text string) error
}

// LarkMessageTool sends a plain text message to a Lark chat.
// Designed for sub-agents that need to notify users but don't have
// direct channel access.
type LarkMessageTool struct {
	sender DirectSender
}

func NewLarkMessageTool(sender DirectSender) *LarkMessageTool {
	return &LarkMessageTool{sender: sender}
}

func (t *LarkMessageTool) Name() string        { return "lark_message" }
func (t *LarkMessageTool) Description() string { return "Send a text message to a Lark chat" }
func (t *LarkMessageTool) FullDescription() string {
	return "Send a text message to a Lark/Feishu chat by channel ID. " +
		"Use this to notify users about task progress or completion. " +
		"The channel_id is provided in your task context — do not call lark_list_chats."
}

var larkMessageParams = json.RawMessage(`{
	"type": "object",
	"properties": {
		"channel_id": {"type": "string", "description": "The channel ID of the target Lark chat"},
		"text": {"type": "string", "description": "The message text to send"}
	},
	"required": ["channel_id", "text"]
}`)

func (t *LarkMessageTool) Parameters() json.RawMessage { return larkMessageParams }

func (t *LarkMessageTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		ChannelID string `json:"channel_id"`
		Text      string `json:"text"`
	}
	if errMsg := tools.ParseArgs(args, &params); errMsg != "" {
		return errMsg, nil
	}
	if params.ChannelID == "" {
		return "Error: 'channel_id' is required", nil
	}
	if params.Text == "" {
		return "Error: 'text' is required", nil
	}

	if err := t.sender.SendDirect(ctx, params.ChannelID, params.Text); err != nil {
		return "Error sending message: " + err.Error(), nil
	}
	return fmt.Sprintf("Message sent to %s", params.ChannelID), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tools/builtin/ -run TestLarkMessageTool -v`
Expected: PASS

- [ ] **Step 5: Register lark_message in BuildToolRegistry**

Modify `internal/app/app.go`. After the existing Lark tools block (around line 804, after `registry.Expand("chat_history")`), add:

```go
		registry.Register(builtin.NewLarkMessageTool(larkCh))
		registry.Expand("lark_message")
```

This goes inside the `if larkCh != nil` block, after line 809.

**Note:** The spec says to register only in the sub-agent factory, but `BuildToolRegistry` is shared by both factories. Registering here makes `lark_message` available to all sessions (main + sub-agent). This is acceptable — the skill prompt constrains usage, and the tool is generic enough to be useful in other contexts. The spec's intent (prevent main agent from sending notifications directly) is enforced by the skill, not by tool visibility.

- [ ] **Step 6: Run full test suite**

Run: `go vet ./... && go test ./internal/tools/builtin/ -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/tools/builtin/lark_message_tool.go internal/tools/builtin/lark_message_tool_test.go internal/app/app.go
git commit -m "feat: add lark_message tool for sub-agent Lark notifications"
```

---

## Chunk 2: Bug Fix — Slash Commands Bypass Per-Chat Queue

### Task 2: Intercept slash commands before enqueueing

**Files:**
- Modify: `internal/app/app.go:158-182` (processMessages method)
- Create: `internal/app/app_slash_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/app/app_slash_test.go`. This test verifies that slash commands are handled even when the per-chat worker is blocked.

The key behavior to test: when a slash command arrives for a remote channel, it should be handled inline on the dispatch goroutine, not sent to the per-chat queue. We test this by verifying that `handleSlashCommand` is called before enqueueing.

Since `processMessages` is tightly coupled to the `AgentInstance`, write a focused test that verifies the routing logic:

```go
package app

import "testing"

func TestSlashCommandDetection(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		isCommand bool
	}{
		{"status command", "/status", true},
		{"help command", "/help", true},
		{"new command", "/new", true},
		{"unknown slash", "/unknown", false},
		{"regular message", "fix issue #123", false},
		{"slash in middle", "please /help me", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isSlash := isRemoteSlashCommand(tt.text)
			if isSlash != tt.isCommand {
				t.Errorf("isRemoteSlashCommand(%q) = %v, want %v", tt.text, isSlash, tt.isCommand)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/ -run TestSlashCommand -v`
Expected: FAIL — `isRemoteSlashCommand` not defined

- [ ] **Step 3: Add the helper function**

Add to `internal/app/app.go` (near the `handleSlashCommand` method, around line 275):

```go
// isRemoteSlashCommand checks if a message is a known slash command
// that can be handled without going through the per-chat message queue.
func isRemoteSlashCommand(text string) bool {
	if !strings.HasPrefix(text, "/") {
		return false
	}
	cmd := strings.TrimSpace(strings.TrimPrefix(text, "/"))
	parts := strings.SplitN(cmd, " ", 2)
	switch parts[0] {
	case "help", "new", "status":
		return true
	}
	return false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/ -run TestSlashCommand -v`
Expected: PASS

- [ ] **Step 5: Modify processMessages to intercept slash commands**

Modify `internal/app/app.go`, the `processMessages` method. Change the remote message handling block (lines 167-182) from:

```go
		q, ok := chatQueues[msg.ChannelID]
		if !ok {
			...
		}
		q <- msg
```

To:

```go
		// Handle slash commands inline, bypassing the per-chat queue.
		// This allows /status to work even when the per-chat worker
		// is blocked in WaitForAny() during sub-agent execution.
		if isRemoteSlashCommand(msg.Text) {
			ch, ok := inst.Channels[msg.ChannelName]
			if ok {
				inst.handleSlashCommand(ctx, ch, msg)
			}
			continue
		}

		q, ok := chatQueues[msg.ChannelID]
		if !ok {
			...
		}
		q <- msg
```

Note: `handleSlashCommand` calls `sess.StatusInfo()` which reads session fields without the mutex. This is a benign data race — usage counters may be slightly stale, which is acceptable for status display. Add a comment to `StatusInfo()` in `session.go` documenting this:

```go
// StatusInfo returns a human-readable summary of the session.
// NOTE: This method intentionally does NOT acquire s.mu so it can be called
// concurrently while a ReAct loop is running (e.g., from /status commands).
// Usage counters may be slightly stale, which is acceptable for display.
```

- [ ] **Step 6: Add data race comment to StatusInfo**

Edit `internal/agent/session.go`, find the `StatusInfo()` method and add the comment before it:

```go
// StatusInfo returns a human-readable summary of the session.
// NOTE: This method intentionally does NOT acquire s.mu so it can be called
// concurrently while a ReAct loop is running (e.g., from /status commands).
// Usage counters may be slightly stale, which is acceptable for display.
```

- [ ] **Step 7: Run tests**

Run: `go vet ./... && go test ./internal/app/ -v`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/app/app.go internal/app/app_slash_test.go internal/agent/session.go
git commit -m "fix: allow /status to bypass per-chat queue during sub-agent execution"
```

---

## Chunk 3: fix_pr Skill

### Task 3: Create the fix_pr skill

**Files:**
- Create: `~/.golem/agents/lark-bot/skills/fix-pr/SKILL.md`

- [ ] **Step 1: Create skill directory**

```bash
mkdir -p ~/.golem/agents/lark-bot/skills/fix-pr
```

- [ ] **Step 2: Write the skill file**

Create `~/.golem/agents/lark-bot/skills/fix-pr/SKILL.md`:

```markdown
---
name: fix-pr
description: Fix a GitHub issue end-to-end — analyze, create PR, wait for review, merge. Uses TiDB for task persistence and sub-agents for async execution.
---

# Fix PR Skill

You fix GitHub issues end-to-end: analyze the issue, write the fix, create a PR, address review comments, and merge. Tasks are tracked in TiDB and executed by sub-agents.

## TiDB Connection

Connect via mysql CLI. Connection details are in environment variables:

```bash
mysql -h "$TIDB_HOST" -P "$TIDB_PORT" -u "$TIDB_USER" -p"$TIDB_PASSWORD" "$TIDB_DATABASE" -e "SQL HERE"
```

## Schema

Ensure these tables exist before first use (run via shell):

```sql
CREATE TABLE IF NOT EXISTS async_tasks (
    id          VARCHAR(36) PRIMARY KEY,
    channel_id  VARCHAR(255) NOT NULL,
    issue_url   VARCHAR(512) NOT NULL,
    prompt      TEXT NOT NULL,
    status      VARCHAR(32) NOT NULL DEFAULT 'pending',
    result      TEXT,
    error       TEXT,
    retry_count INT NOT NULL DEFAULT 0,
    max_retries INT NOT NULL DEFAULT 3,
    deadline    TIMESTAMP NULL,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_status (status),
    UNIQUE INDEX idx_issue_url (issue_url)
);

CREATE TABLE IF NOT EXISTS task_events (
    id         BIGINT AUTO_INCREMENT PRIMARY KEY,
    task_id    VARCHAR(36) NOT NULL,
    phase      VARCHAR(64) NOT NULL,
    detail     TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_task (task_id)
);
```

---

## For Main Agent

When a user asks you to fix a GitHub issue, follow these steps exactly.

### Step 1: Extract the issue URL

Extract the GitHub issue URL from the user's message. It must match the pattern `https://github.com/<owner>/<repo>/issues/<number>`. If the user only provides a number (e.g., "#123"), you need the repo context to construct the full URL.

### Step 2: Check for existing task

```bash
mysql ... -e "SELECT id, status, result, error, retry_count FROM async_tasks WHERE issue_url='<issue_url>' LIMIT 1"
```

Based on the result:
- **No rows** → proceed to Step 3 (create new task)
- **status=running** → check if you have an active sub-agent for this task. If yes, reply "任务已在执行中，ID: <id>". If no active sub-agent (process was restarted), proceed to Step 4 (recovery).
- **status=completed** → reply with the stored result
- **status=failed** → ask the user if they want to retry. If yes, reset: `UPDATE async_tasks SET status='pending', retry_count=0, error=NULL WHERE id='<id>'` and proceed to Step 3.

### Step 3: Create task record

```bash
mysql ... -e "INSERT INTO async_tasks (id, channel_id, issue_url, prompt, status, deadline) VALUES ((SELECT UUID()), '<channel_id>', '<issue_url>', '<user_message>', 'pending', DATE_ADD(NOW(), INTERVAL 12 HOUR))"
mysql ... -e "SELECT id FROM async_tasks WHERE issue_url='<issue_url>'"
```

### Step 4: Spawn sub-agent

**CRITICAL:** You MUST NOT do any fix work yourself. Your only job is to create the task record and spawn the sub-agent.

Call `spawn_agent` with the following prompt template. Replace placeholders with actual values:

```text
你是一个异步任务执行器。请使用 fix-pr skill 来完成以下任务。

- Task ID: <task_id>
- Channel ID: <channel_id>
- Issue URL: <issue_url>
- TiDB connection: mysql -h "$TIDB_HOST" -P "$TIDB_PORT" -u "$TIDB_USER" -p"$TIDB_PASSWORD" "$TIDB_DATABASE" -e

请先调用 skill tool 加载 fix-pr skill，然后按照 "For Sub-Agent" section 的 workflow 执行。
```

For recovery (orphaned running task), also query and append the task event history to the prompt:
```bash
mysql ... -e "SELECT phase, detail, created_at FROM task_events WHERE task_id='<task_id>' ORDER BY created_at"
```

Append to the spawn_agent prompt:
```text
这是一个恢复任务。以下是之前的执行历史，请根据历史判断从哪一步继续：
<event_history>
```

### Step 5: Wait and report

After the sub-agent returns, query TiDB for the task result and reply to the user:
```bash
mysql ... -e "SELECT status, result, error FROM async_tasks WHERE id='<task_id>'"
```

### Status query

When a user asks about a task status (e.g., "任务 xxx 状态" or "task xxx status"):
```bash
mysql ... -e "SELECT id, status, result, error, retry_count, created_at, updated_at FROM async_tasks WHERE id='<task_id>'"
mysql ... -e "SELECT phase, detail, created_at FROM task_events WHERE task_id='<task_id>' ORDER BY created_at DESC LIMIT 5"
```

Reply with a formatted summary of the task state and recent events.

---

## For Sub-Agent

This section is read by the sub-agent after it loads the fix-pr skill. It defines the workflow and behavioral constraints.

### Task Context

Your task parameters are provided in the spawn_agent prompt:
- **Task ID** — unique identifier for this task in TiDB
- **Channel ID** — Lark chat to send notifications to via `lark_message` tool
- **Issue URL** — GitHub issue to fix
- **TiDB connection** — mysql CLI connection string for task state updates

### Workflow

Follow these steps in order. Update `task_events` at each phase transition.

**1. Mark task as running and notify user**

```bash
mysql <tidb_connection> -e "UPDATE async_tasks SET status='running' WHERE id='<task_id>'"
```

Use `lark_message` tool:
- First attempt: `{"channel_id": "<channel_id>", "text": "任务已开始，ID: <task_id>\nIssue: <issue_url>"}`
- On retry: `{"channel_id": "<channel_id>", "text": "任务重试中 (第N次)，ID: <task_id>"}`

**2. Analyze the issue**

```bash
mysql <tidb_connection> -e "INSERT INTO task_events (task_id, phase) VALUES ('<task_id>', 'analyzing')"
gh issue view <issue_url> --json title,body,labels,comments
```

Read the issue carefully. Understand the problem, expected behavior, and any reproduction steps.

**3. Fix the issue**

- Clone the repository if needed
- Create a branch: `fix/<issue_number>`
- Analyze the codebase to find the root cause
- Write the fix
- Write or update tests
- Create a PR:
  ```bash
  gh pr create --title "fix: <description>" --body "Fixes <issue_url>"
  ```

```bash
mysql <tidb_connection> -e "INSERT INTO task_events (task_id, phase, detail) VALUES ('<task_id>', 'pr_created', 'PR #<number>')"
```

**4. Wait for review**

Poll loop — max 72 iterations (12 hours at 10-minute intervals):

```bash
sleep 600
gh pr view <pr_number> --json reviewDecision,reviews,comments
```

- If there are new review comments → address them, push changes
- If `reviewDecision` is `APPROVED` → break out of loop
- Check deadline:
  ```bash
  mysql <tidb_connection> -e "SELECT deadline < NOW() AS expired FROM async_tasks WHERE id='<task_id>'"
  ```
  If expired → treat as timeout failure

```bash
mysql <tidb_connection> -e "INSERT INTO task_events (task_id, phase) VALUES ('<task_id>', 'waiting_review')"
```

**5. Merge**

```bash
gh pr merge <pr_number> --squash --delete-branch
mysql <tidb_connection> -e "INSERT INTO task_events (task_id, phase, detail) VALUES ('<task_id>', 'merged', 'merge commit: <sha>')"
```

**6. Complete**

```bash
mysql <tidb_connection> -e "UPDATE async_tasks SET status='completed', result='PR #<number> merged. Commit: <sha>' WHERE id='<task_id>'"
```

Exit. The main agent will read the result and notify the user.

### Failure handling

If any step fails:

```bash
mysql <tidb_connection> -e "UPDATE async_tasks SET retry_count=retry_count+1 WHERE id='<task_id>'"
mysql <tidb_connection> -e "INSERT INTO task_events (task_id, phase, detail) VALUES ('<task_id>', 'retry', '<error description>')"
mysql <tidb_connection> -e "SELECT retry_count, max_retries FROM async_tasks WHERE id='<task_id>'"
```

- If `retry_count < max_retries` → restart from step 1
- If reached limit:
  ```bash
  mysql <tidb_connection> -e "UPDATE async_tasks SET status='failed', error='<error>' WHERE id='<task_id>'"
  ```
  Use `lark_message`: `{"channel_id": "<channel_id>", "text": "任务失败，ID: <task_id>\n原因: <error>"}`
  Exit.

### Recovery

If your prompt contains "Previous execution history", review it and skip steps already completed. For example, if `pr_created` event exists, skip directly to step 4 (wait for review).
```

- [ ] **Step 3: Verify skill is discoverable**

Run: `go run ./cmd/golem/ --agent lark-bot --dry-run 2>&1 | grep -i "fix-pr"` (or equivalent verification that the skill shows up in the tool list)

If no `--dry-run` flag exists, verify by checking the skill store discovers the directory:
```bash
ls ~/.golem/agents/lark-bot/skills/fix-pr/SKILL.md
```

- [ ] **Step 4: Commit**

The skill file lives outside the repo (in `~/.golem/`), so no git commit needed. But document its location:

```bash
echo "Skill created at ~/.golem/agents/lark-bot/skills/fix-pr/SKILL.md"
```

---

## Chunk 4: Integration Verification

### Task 4: End-to-end smoke test

- [ ] **Step 1: Verify lark_message tool compiles and registers**

```bash
go build ./...
```

Expected: No compilation errors

- [ ] **Step 2: Run all tests**

```bash
go vet ./... && go test ./... -race
```

Expected: All PASS

- [ ] **Step 3: Verify the slash command fix works**

Manual verification steps:
1. Start the Lark bot: `go run ./cmd/golem/ --agent lark-bot`
2. Send a message that triggers `spawn_agent` (any long task)
3. While the sub-agent is running, send `/status` in the same Lark chat
4. Verify `/status` responds immediately without waiting for the sub-agent

- [ ] **Step 4: Verify the fix-pr skill loads**

1. Start the Lark bot
2. Check logs for skill discovery of `fix-pr`
3. Send "fix https://github.com/owner/repo/issues/1" in Lark
4. Verify the agent creates a TiDB record and spawns a sub-agent

---

## Summary

| Task | Description | Estimated Steps |
|------|-------------|----------------|
| 1 | `lark_message` tool + tests + registration | 7 steps |
| 2 | Slash command bypass bug fix + tests | 8 steps |
| 3 | `fix_pr` skill file | 4 steps |
| 4 | Integration verification | 4 steps |
