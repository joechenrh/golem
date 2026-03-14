# Async Task System — Skill-Driven Design

## Overview

Add the ability for the Lark bot to accept long-running tasks (e.g., "fix issue #123") via natural language, execute them asynchronously through sub-agents, and report results back to the user. The system is **skill-driven**: orchestration logic lives in a skill prompt, not in bespoke infrastructure code.

### Design Principles

- **Zero new task-management tools** — the agent uses `shell` + `mysql` CLI to operate on TiDB directly, guided by the skill.
- **One new internal tool** — `lark_message`, a thin wrapper around `SendDirect`, giving sub-agents the ability to message a Lark chat.
- **Skill as orchestrator** — the `fix_pr` skill defines the workflow for both the main agent and sub-agent, including TiDB schema, SQL templates, and behavioral constraints.
- **Existing infrastructure reused** — `spawn_agent`, `shell`, `sleep` (Linux), `gh` CLI.

## Architecture

```
User → Lark chat (channel_id=xxx)
  ↓
Main Session Agent (triggers fix_pr skill)
  ├── shell: mysql — query/create task record in TiDB
  ├── spawn_agent(prompt with skill + channel_id + task_id)
  │     ↓
  │   Sub-Agent (follows fix_pr skill — sub-agent section)
  │     ├── lark_message(channel_id, "已收到，任务 ID: abc-123")
  │     ├── shell: mysql — update task_events
  │     ├── shell: gh / git — analyze, fix, create PR
  │     ├── shell: sleep + gh pr view — poll for review
  │     ├── shell: gh pr merge
  │     ├── shell: mysql — update status=completed
  │     └── exit → result returns to main session
  │     ↓
  └── Main session receives sub-agent result
      └── Queries TiDB, replies to user with final result
```

## Data Model (TiDB)

Two tables. Schema is defined in the skill file; the agent creates them via `mysql` if they don't exist.

### async_tasks

```sql
CREATE TABLE IF NOT EXISTS async_tasks (
    id          VARCHAR(36) PRIMARY KEY,
    channel_id  VARCHAR(255) NOT NULL,
    issue_url   VARCHAR(512) NOT NULL,            -- GitHub issue URL, used for dedup lookup
    prompt      TEXT NOT NULL,
    status      VARCHAR(32) NOT NULL DEFAULT 'pending',  -- pending/running/completed/failed
    result      TEXT,
    error       TEXT,
    retry_count INT NOT NULL DEFAULT 0,
    max_retries INT NOT NULL DEFAULT 3,
    deadline    TIMESTAMP NULL,                   -- max wall-clock time; skill enforces this
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_status (status),
    UNIQUE INDEX idx_issue_url (issue_url)
);
```

**Notes on SQL usage:** The skill instructs the agent to generate UUIDs via `SELECT UUID()` and to use single-quoted string literals for values. Since all SQL is executed by the LLM via `mysql -e`, there is an inherent SQL injection surface from malformed user input embedded in prompts. This is an accepted tradeoff of the skill-driven approach. The `issue_url` field is extracted and validated by the LLM (must match `https://github.com/.../issues/\d+`), and the `prompt` field is the only free-text user input stored.

### task_events

```sql
CREATE TABLE IF NOT EXISTS task_events (
    id         BIGINT AUTO_INCREMENT PRIMARY KEY,
    task_id    VARCHAR(36) NOT NULL,
    phase      VARCHAR(64) NOT NULL,
    detail     TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_task (task_id)
);
```

**Purpose of task_events:** Provides execution history for recovery after process restart. When a user re-triggers a task that was previously running, the sub-agent receives these events as context and decides where to continue.

## New Tool: lark_message

A single new internal tool registered only for sub-agent sessions.

- **Name:** `lark_message`
- **Parameters:** `channel_id` (string), `text` (string)
- **Behavior:** Calls `channel.SendDirect(ctx, channelID, text)` on the Lark channel.
- **Registration:** Only in the sub-agent tool factory (the `base` factory in app.go), not in the main session's tool set.
- **Location:** `internal/tools/builtin/lark_message.go`

This tool is intentionally generic — it sends a message to any Lark chat, not tied to the async task system.

## Skill: fix_pr

One skill file with two sections.

### Main Agent Section

Trigger: user message involves fixing a GitHub issue.

Behavioral constraints:
1. Query `async_tasks` by `issue_url`.
   - If `status=running` **and** the task was created in the current process (i.e., the sub-agent is tracked in `TaskTracker`) → reply "任务已在执行中，ID: xxx"
   - If `status=running` **but** no active sub-agent exists (process restarted) → treat as orphaned, proceed to recovery (see Recovery section)
   - If `status=completed` → reply with the stored result
   - If `status=failed` → ask user whether to retry
   - If not found → `INSERT` a new record with `status=pending`, `deadline=NOW()+12h`
2. Call `spawn_agent` with a prompt containing:
   - The sub-agent section of this skill
   - `task_id`, `channel_id`, issue URL
   - TiDB connection info and SQL templates
3. **Do NOT** send any acknowledgment to the user — the sub-agent handles this via `lark_message`.
4. **MUST NOT** perform any fix work itself — only create the task and spawn.
5. When the sub-agent returns, query TiDB and reply to user with final result.

**Important limitation:** While the sub-agent is running, the main session's per-chat worker is blocked in `WaitForAny()`. The user cannot send regular messages to this chat session until the sub-agent completes. Slash commands (e.g., `/status`) are exempt after the bug fix below. This also means **only one task can run per chat at a time** (implicit per-chat concurrency limit of 1). This is an accepted tradeoff for v1 — future versions may release the worker during `WaitForAny`.

### Sub-Agent Section

Workflow (skill prompt drives the agent to follow this sequence):

1. `mysql: UPDATE async_tasks SET status='running' WHERE id='{task_id}'`
2. `lark_message(channel_id, "任务已开始，ID: {task_id}")` — on retry, send "任务重试中 (第N次)，ID: {task_id}" instead
3. `mysql: INSERT INTO task_events (task_id, phase) VALUES ('{task_id}', 'analyzing')`
4. `gh issue view {issue_url}` — understand the problem
5. Clone repo, create branch, analyze code, write fix, create PR
6. `mysql: INSERT INTO task_events (task_id, phase, detail) VALUES ('{task_id}', 'pr_created', 'PR #xxx')`
7. Poll loop (max 72 iterations = 12 hours at 10-min intervals; enforced by `deadline` column):
   - `sleep 600` (10 minutes)
   - `gh pr view --json reviewDecision,reviews`
   - If review comments → address them, push
   - If approved → break
   - Check deadline: `mysql: SELECT deadline FROM async_tasks WHERE id='{task_id}'` — if past, treat as timeout failure
   - `mysql: INSERT INTO task_events (task_id, phase) VALUES ('{task_id}', 'waiting_review')`
8. `gh pr merge`
9. `mysql: UPDATE async_tasks SET status='completed', result='...' WHERE id='{task_id}'`
10. Exit — result flows back to main session.

**Result size:** The skill instructs the sub-agent to store a concise summary in `result` (task ID, PR number, merge commit), not full diffs or logs.

Failure handling (enforced by skill):
- Any step error → `UPDATE async_tasks SET retry_count=retry_count+1` + `INSERT task_events (phase='retry', detail='...')`
- `retry_count < max_retries` → restart from step 1
- Reached limit → `UPDATE status='failed'` → `lark_message` notify user → exit

### Recovery After Process Restart

No automatic recovery on startup. Recovery is triggered by the user re-sending a message (e.g., "fix #123" or "任务 xxx 状态").

Recovery is triggered when the main agent section (step 1) queries by `issue_url` and finds a task with `status=running`. It distinguishes "alive" from "orphaned" using `TaskTracker` — an existing in-memory component in `internal/agent/tasks.go` that tracks all active sub-agents spawned by `spawn_agent`. No new code needed for this check.

1. Main agent queries `async_tasks` by `issue_url` — finds `status=running`
2. Check `TaskTracker` for an active sub-agent associated with this task — if found, the task is alive, reply "已在执行中"
3. If no active sub-agent exists (orphaned after restart):
   - Query `task_events ORDER BY created_at` for the task's execution history
   - Spawn a new sub-agent with the history injected into the prompt as context
   - The sub-agent reads the history and decides where to continue (e.g., "PR already created, skip to waiting for review")

Note: the "status query" path (user asks "任务 xxx 状态") only reads and displays task state — it does NOT trigger recovery. Recovery is only triggered via the `issue_url` lookup path.

### Status Query

When user asks about a task (e.g., "任务 xxx 状态"), the main agent:
1. `mysql: SELECT * FROM async_tasks WHERE id='{task_id}'`
2. `mysql: SELECT * FROM task_events WHERE task_id='{task_id}' ORDER BY created_at DESC LIMIT 5`
3. Reply with current status and recent events.

## Bug Fix: /status Blocked During Sub-Agent Execution

### Problem

When a sub-agent is running via `spawn_agent`, the user cannot query `/status` in Lark until the sub-agent completes. Three layers of blocking combine:

1. **Per-chat message queue** (app.go) — one worker goroutine per chat, processes messages sequentially.
2. **Session mutex** (session.go:245-246) — `HandleInput()` holds `s.mu` for the entire ReAct loop.
3. **WaitForAny()** (react.go:190-200) — ReAct loop blocks waiting for background tasks to complete.

A `/status` message enters the per-chat queue but cannot be processed until the worker finishes the current `processMessage()` call, which is stuck in `WaitForAny()`.

### Fix

Intercept slash commands at the message dispatch layer in `app.go`, **before the message is sent to the per-chat queue** (before `q <- msg` at the point where messages are routed to per-chat worker goroutines):

```
Message received from inCh → router.RouteUser(msg.Text)
    ├── is slash command → handle inline on the dispatch goroutine
    │   (call sess.StatusInfo() etc. directly, send response via channel)
    └── not a command  → enqueue to per-chat queue → normal ReAct loop
```

**Data race note:** `StatusInfo()` reads `s.sessionUsage` without acquiring `s.mu`, while `runReActLoop` writes it under the mutex. This is a benign race — usage counters being slightly stale is acceptable for status display. Add a code comment documenting this. If stricter correctness is needed later, switch usage fields to `atomic` operations.

**Change location:** `internal/app/app.go` — the message fan-out section where `chatQueues` are managed, before the `q <- msg` send.

## Future Enhancements (Not Implemented)

- **Concurrency control:** Global limit on running tasks. Note that v1 already has an implicit per-chat limit of 1 (the main session blocks in `WaitForAny` during sub-agent execution). Cross-chat limits can be implemented by checking `SELECT COUNT(*) FROM async_tasks WHERE status='running'` before creating a new task, enforced in the skill prompt.
- **Checkpoint recovery:** Instead of restarting from scratch on failure, persist intermediate state (e.g., branch name, PR number) in `task_events` and resume from the last checkpoint. The `task_events` table already supports this — the skill prompt just needs to be enhanced.
- **Fine-grained progress notifications:** Sub-agent sends periodic `lark_message` updates at each phase change, not just start/end.

## Change Summary

| Category | Change | Files |
|----------|--------|-------|
| New tool | `lark_message`: wraps `SendDirect`, params `channel_id` + `text`, sub-agent only | `internal/tools/builtin/lark_message.go` |
| New skill | `fix_pr`: main agent + sub-agent sections, TiDB schema, SQL templates, workflow | skill directory |
| Bug fix | Slash commands intercepted before per-chat queue | `internal/app/app.go` |
| TiDB | `async_tasks` + `task_events` tables | Schema in skill file |
