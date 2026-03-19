# Async Task Optimizations Design

## Overview

Optimize the async task system based on observed issues from real fix-pr executions. Improvements are split into two groups based on the principle: **code enforces reliability-critical behavior, skill handles flexibility-critical behavior.**

## Group A: Code Enforcements

### A1. Shell Output Head+Tail Truncation

**Problem:** `go test` output dumps ~19K tokens into context. Current `TruncateWithNote` keeps the first 50KB, but useful info (PASS/FAIL) is at the end.

**Fix:** New `TruncateHeadTail(s string, maxBytes int) string` in `stringutil/truncate.go`. When `len(s) > maxBytes`, keep first `maxBytes/2` + `"\n... [truncated N bytes] ...\n"` + last `maxBytes/2`. Replace `TruncateWithNote` usage in `executor/local.go` with `TruncateHeadTail`.

**Files:** `internal/stringutil/truncate.go`, `internal/executor/local.go`

### A2. Ephemeral Session Disable Nudge

**Problem:** After sub-agent completes (final answer with tools=0), nudge mechanism pushes 4 more iterations (~260K wasted tokens). Sub-agents don't need nudge — giving a final answer means the task is done.

**Fix:** Add `DisableNudge bool` to `Session` or `Config`. Set it to true in `buildEphemeralSession`. The nudge check in `react.go` skips when this flag is set. If nudge is already disabled when `classifierLLM == nil`, verify this and document it — no code change needed.

**Files:** `internal/agent/react.go` or `internal/agent/session.go`, `internal/app/app.go`

### A3. Readable Task Names via `description` Parameter

**Problem:** `/status` shows raw prompt as task name ("你是一个异步任务执行器..."). Unreadable.

**Fix:** Add optional `description` parameter to `spawn_agent` tool schema. In `Execute()`, use `description` as task desc if provided, fallback to `truncateDesc(prompt, 100)`. The LLM provides a human-readable summary like "fix #67041" when spawning.

```json
{
  "type": "object",
  "properties": {
    "prompt": {"type": "string", "description": "..."},
    "context": {"type": "string", "description": "..."},
    "description": {"type": "string", "description": "Short human-readable task name for status display, e.g. 'fix #67041'"}
  },
  "required": ["prompt"]
}
```

**Files:** `internal/tools/builtin/spawn_tool.go`

### A4. `/status` Tree with Duration

**Problem:** Tree only shows `[running]` — no sense of progress or time.

**Fix:** `TreeSummary` shows duration since `StartedAt` for running tasks, and time since `CompletedAt` for completed tasks:

```
Background tasks:
|- #1 fix #67041 [running 3m22s]
   |- #1 analyze root cause [completed 1m15s]
```

Use `time.Since(t.StartedAt).Truncate(time.Second)` for running, `time.Since(t.CompletedAt).Truncate(time.Second)` for completed/failed. `StartedAt` and `CompletedAt` already exist on `BackgroundTask`.

**Files:** `internal/agent/tasks.go` (TreeSummary method)

## Group B: Skill Optimizations

### B1. Nested Spawn for Phased Execution

**Problem:** A single sub-agent exhausts context on analysis (~500K tokens reading code), leaving no room for implementation. The nested spawn capability exists but the skill doesn't guide the agent to use it.

**Fix:** Update `fix-pr` skill's sub-agent section to mandate a two-phase approach:

1. **Phase 1 — Analyze:** Spawn a sub-sub-agent to read the issue, search code, reproduce, and return a concise root cause summary (target: <500 tokens output).
2. **Phase 2 — Fix:** Spawn another sub-sub-agent with the root cause summary as context. It writes the fix, tests, and creates the PR.

Each sub-sub-agent has a fresh context (~5K starting prompt), so total token usage drops from ~1M to ~300K.

Skill prompt additions:
- "You MUST use spawn_agent to delegate analysis and fix phases separately"
- "Each spawn_agent call MUST include a `description` parameter"
- "The analysis agent returns ONLY: root cause location (file:line), one-sentence explanation, suggested fix approach"

**Files:** `~/.golem/agents/lark-bot/skills/fix-pr/SKILL.md`

### B2. Git/PR Script Template

**Problem:** Git/PR operations take 8 iterations (8 × 65K prompt = ~520K tokens). Each operation is trivial but the LLM explores step by step.

**Fix:** Skill provides a ready-to-run shell script template for the final phase:

```bash
set -e
cd <repo_dir>
git add -A
git commit -m "<commit_msg>"
if ! git remote | grep -q '^fork$'; then
  git remote add fork https://github.com/<user>/<repo>.git
fi
git push -u fork <branch>
gh pr create --repo <upstream> --head <user>:<branch> --base master \
  --title "<title>" --body "<body>"
```

Sub-agent fills in the placeholders and runs it in one shell call.

**Files:** `~/.golem/agents/lark-bot/skills/fix-pr/SKILL.md`

### B3. Reuse Running TiDB for Reproduction

**Problem:** Sub-agent compiles and runs TiDB from source for reproduction (~154s). But a TiDB instance is already running at 127.0.0.1:4000.

**Fix:** Skill explicitly instructs: "To reproduce SQL issues, connect to the running TiDB instance at 127.0.0.1:4000 using the mysql CLI. Do NOT compile TiDB from source for reproduction."

**Files:** `~/.golem/agents/lark-bot/skills/fix-pr/SKILL.md`

## Change Summary

| # | Category | Change | Files |
|---|----------|--------|-------|
| A1 | Code | Head+tail truncation for shell output | `stringutil/truncate.go`, `executor/local.go` |
| A2 | Code | Disable nudge for ephemeral sessions | `agent/react.go` or `session.go`, `app/app.go` |
| A3 | Code | `description` param on spawn_agent | `tools/builtin/spawn_tool.go` |
| A4 | Code | Duration in TreeSummary | `agent/tasks.go` |
| B1 | Skill | Two-phase nested spawn in fix-pr | `fix-pr/SKILL.md` |
| B2 | Skill | Git/PR script template | `fix-pr/SKILL.md` |
| B3 | Skill | Reuse running TiDB | `fix-pr/SKILL.md` |

## Ordering

Group A first (code enforcements, immediate reliability gains), then Group B (skill optimizations, require prompt tuning and testing).

Within Group A, tasks are independent — can be done in any order or parallel.
