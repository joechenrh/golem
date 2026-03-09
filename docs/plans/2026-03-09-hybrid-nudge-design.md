# Hybrid Nudge System with LLM Classifier Escalation

## Problem

The current nudge system uses hardcoded phrase matching to detect when the LLM
is describing a plan instead of calling tools. Analysis of production logs
(lark-bot session 2026-03-07) reveals three failure modes:

1. **False negatives**: Plan phrases appearing beyond the 200-char prefix escape
   detection (e.g., "我现在就重写并覆盖" at char ~300). The plan is accepted as
   a final answer, forcing the user to say "继续" repeatedly.

2. **False positives**: Conversational phrases like "收到" (acknowledged) trigger
   nudges on non-planning responses.

3. **Stuck loops**: When the LLM is genuinely lost (not lazy), nudging with
   "call a tool" makes it worse. The LLM gives more placeholder responses,
   exhausts the nudge budget, and the user sees 8+ rounds of "继续" with no
   progress.

The core insight: nudges work when the LLM knows what to do but is being lazy.
They fail when the LLM is genuinely lost. The current system cannot tell the
difference.

## Design

### Three-Tier Decision Flow

When the LLM returns a text-only response (no tool calls):

```
Response (no tool calls)
    |
    +-- Empty? --> existing empty-retry logic (unchanged)
    |
    +-- Clear plan phrase in prefix? --> NUDGE (existing heuristic)
    |
    +-- Ambiguous? --> Call classifier model --> returns one of:
    |     +-- "nudge"  --> inject nudge message (as today)
    |     +-- "accept" --> accept as final answer
    |     +-- "stuck"  --> inject task reminder (new)
    |
    +-- Long response, no phrase match --> ACCEPT (as today)
```

### Ambiguity Detection Gate

The classifier fires only when heuristics are inconclusive:

```go
isAmbiguous := !looksLikePlan(resp.Content) &&
    len(resp.Content) < 100 &&
    hasToolHistory(s.tape)
```

- `100` char threshold: production stuck responses were 4-70 tokens (very short).
- `hasToolHistory`: if the session never used tools, text-only responses are normal.
- Skip classifier on second nudge: if `nudges >= 1` and still no tool call, go
  straight to "stuck" -- no point classifying twice.

The classifier fires at most once per user turn.

### Classifier Model

A separate, cheaper model configured globally:

```bash
# ~/.golem/config.env
GOLEM_CLASSIFIER_MODEL=openai:gpt-4o-mini
```

- New `ClassifierModel` field in `config.Config`.
- If unset, classifier is disabled; falls back to heuristic-only (backward compatible).
- A second `llm.Client` is built at startup, sharing API keys and base URLs.

### Classifier Prompt

Minimal context -- not the full conversation:

```
System: You are a response classifier for an AI agent that has tools available.
Given the agent's response and the user's last message, classify the situation.

Respond with JSON only:
{"decision": "nudge" | "accept" | "stuck", "task_summary": "..."}

Rules:
- "nudge": The agent is describing a plan instead of acting. It knows what to do
  but didn't call a tool.
- "accept": The agent's response is a valid final answer (explanation,
  confirmation, clarification question).
- "stuck": The agent is repeating itself, giving empty promises, or clearly lost.
  When stuck, write a 1-sentence task_summary of what the user actually wants done.

task_summary is only required when decision is "stuck".
```

User message to classifier:

```
User's last message: {lastUserMessage}
Agent's response: {resp.Content}
Available tools: {toolNameList}
```

Design choices:
- ~200-400 input tokens per call (no conversation history).
- Tool name list included so the classifier knows what actions are possible.
- JSON response; parse failure falls back to heuristic behavior.

### Stuck Recovery: Task Reminder Injection

When classifier returns "stuck" (or `nudges >= 1` with no tool call):

```
English: "You appear stuck. The user wants: {task_summary}. Call the appropriate
tool now to complete this task. Do not explain or acknowledge -- act."

Chinese: "你似乎卡住了。用户需要：{task_summary}。请立即调用工具完成任务，
不要解释或确认，直接执行。"
```

Two-phase escalation replaces the current flat nudge counter:

```
nudge 0: classifier says "nudge" --> generic nudge (as today)
nudge 1: skip classifier, assume stuck --> task reminder injection
nudge 2: still no tool call --> accept as final, give up
```

### Heuristic Improvements

Independent of the classifier, two fixes to reduce false positives/negatives:

1. Increase `planCheckPrefixLen` from 200 to 400 (catches late plan phrases).
2. Remove "收到" from the Chinese phrase list (conversational, not planning).

## Implementation Scope

| File | Change |
|------|--------|
| `internal/config/config.go` | Add `ClassifierModel` field, parse `GOLEM_CLASSIFIER_MODEL` |
| `.env.example` | Add `GOLEM_CLASSIFIER_MODEL` example |
| `internal/app/app.go` | Build second `llm.Client` for classifier, pass to Session |
| `internal/agent/session.go` | Add `classifierLLM` field, three-tier decision flow, stuck escalation |
| `internal/agent/nudge.go` | Update prefix len, remove "收到", add `classifyResponse()`, `taskReminderMessage()` |
| `internal/agent/nudge_test.go` | Tests for new prefix len, removed phrase, classifier logic |
| `design/02-agent-session.md` | Update nudge documentation |

## Risks

- **Classifier latency**: 200-500ms on the ambiguous path. Acceptable since it
  fires at most once per turn and only on short ambiguous responses.
- **Classifier hallucination**: May misclassify. Mitigated by JSON parsing with
  heuristic fallback, and the gate ensuring it only fires on genuinely ambiguous
  cases.
- **task_summary quality**: The classifier sees limited context. Summary may be
  imprecise. Still better than the current generic "call a tool" message.
