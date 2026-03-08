# 05 — Context Manager

## Overview

The context manager (`internal/ctxmgr/`) controls which tape entries become LLM messages before each API call. The tape is an append-only JSONL log containing every message, tool result, anchor, and summary for a session. Because tapes grow without bound, a **context strategy** decides how to compress or trim that history so it fits within the model's context window.

The strategy is invoked in `session.go` inside `executeLLMCall`, where `entries` is the full tape and `maxTokens` comes from `ctxmgr.ModelContextWindow(modelName)`. `ModelContextWindow` maps model name substrings to context sizes: Claude models get 200k, GPT-4 variants get 128k, and unknown models default to 128k. The ReAct loop in `session.runReActLoop` determines `maxTokens` via this function, then `executeLLMCall` reads all tape entries and calls `BuildContext`. If there is a pending (not-yet-persisted) user message, it is appended to the resulting messages slice after strategy processing, and the final slice is sent to the LLM as `ChatRequest.Messages`.

Strategy selection is controlled by the `GOLEM_CONTEXT_STRATEGY` env var or `context-strategy` config file key (default: `"masking"`). The factory `ctxmgr.NewContextStrategy` is called in `app.go` when building the main session, in `manager.go` when creating per-chat sessions via `SessionManager`, and in `app.go` for sub-agent sessions.

## Overhead Budgeting

All strategies now support an `Overhead` field (set via the `OverheadSetter` interface) that subtracts system prompt + tool schema tokens from the context window budget. In `executeLLMCall`, after building `systemPrompt` and `toolDefs`, the overhead is computed via `ctxmgr.EstimateOverhead(systemPrompt, toolDefs)` and set on the strategy. All threshold checks and `trimToFit` then operate on `effectiveMax = maxTokens - Overhead` instead of the raw model window size.

`EstimateOverhead` uses the same `estimateStringTokens` heuristic applied to the system prompt text, tool names, descriptions, and JSON schema parameters.

## ContextStrategy Interface

```go
type ContextStrategy interface {
    BuildContext(ctx context.Context, entries []tape.TapeEntry, maxTokens int) ([]llm.Message, error)
    Name() string
}

type OverheadSetter interface {
    SetOverhead(tokens int)
}
```

`NewContextStrategy(name)` is the factory. It accepts `"anchor"`, `"masking"`, and `"hybrid"`.

## AnchorStrategy

The simplest strategy. It calls `tape.BuildMessages(entries)` to obtain messages, then passes the result through `trimToFit(msgs, effectiveMax)`. No modification of message content occurs; what the tape recorded is what the LLM sees.

Source: `internal/tape/entry.go`

`tape.BuildMessages` scans for the last `KindAnchor` and includes only `KindMessage` entries after it. It also scans backward for the most recent `KindSummary` and, if found, injects it as a leading `[Previous conversation summary]` user message plus an assistant acknowledgment, giving restored sessions carry-forward context. User messages with a `sender_id` field get a `[sender:xxx]` prefix so the LLM can distinguish speakers in group chats.

## MaskingStrategy

Extends the anchor approach with output truncation to reclaim token budget.

| Field            | Default | Purpose                                                       |
|------------------|---------|---------------------------------------------------------------|
| `MaskThreshold`  | `0.5`   | Fraction of `effectiveMax` above which masking activates      |
| `MaxOutputChars` | `2000`  | Max chars per tool output before truncation                   |
| `Overhead`       | `0`     | Set by `OverheadSetter`; subtracted from model context window |

The pipeline first calls `tape.BuildMessages(entries)` (same as Anchor), then runs `EstimateTokens(msgs)`. If the estimate exceeds `effectiveMax * MaskThreshold`, it calls `MaskObservations(msgs, MaxOutputChars)`. Finally, it passes the result through `trimToFit(msgs, effectiveMax)`.

### MaskObservations

`MaskObservations` iterates over messages and truncates any `Role == "tool"` message whose `Content` exceeds `MaxOutputChars`. It keeps the first and last `MaxOutputChars/2` characters, replacing the middle with `\n[...truncated N chars...]\n`. Non-tool messages and short tool outputs are left untouched. The function returns a new slice; originals are not mutated.

## HybridStrategy

The most capable strategy, combining LLM-powered summarization with adaptive masking and an `OnDrop` callback for saving discarded context.

| Field                | Default | Purpose                                                       |
|----------------------|---------|---------------------------------------------------------------|
| `MaskThreshold`      | `0.5`   | Fraction of `effectiveMax` above which masking activates      |
| `SummarizeThreshold` | `0.7`   | Fraction of `effectiveMax` above which LLM summarization runs |
| `MaxOutputChars`     | `2000`  | Max chars per tool output before truncation                   |
| `Overhead`           | `0`     | Set by `OverheadSetter`; subtracted from model context window |
| `LLM`               | `nil`   | LLM client for summarization (set by wiring layer)            |
| `Model`              | `""`    | Model name for summarization calls                            |
| `OnDrop`             | `nil`   | Callback invoked with messages about to be discarded          |

### Pipeline

1. **`tape.BuildMessages(entries)`** — get all post-anchor messages with summary injection.
2. **Summarize** (if `tokens > effectiveMax * SummarizeThreshold` and `LLM != nil`): Take the oldest half of messages, call the LLM to distill them into a structured summary (TOPIC, DECISIONS, OUTCOMES, PENDING, KEY FACTS), and replace them with a synthetic `[Summarized earlier context]` user message plus an assistant acknowledgment.
3. **Mask** (if `tokens > effectiveMax * MaskThreshold`): Run `MaskObservations` on tool outputs.
4. **Trim with callback** (last resort): Drop oldest messages to fit. Before discarding, the `OnDrop` callback is fired in a goroutine with the about-to-be-dropped messages, allowing hooks (e.g. mem9-save) to persist the content externally.

When `LLM` is nil (not wired), step 2 is skipped, and the strategy degrades to mask + trim.

### OnDrop and context_dropped Hook

The `OnDrop` callback is wired in `session.go` / `app.go` / `manager.go` to fire the `context_dropped` external hook event. The hook data contains:
- `dropped_text`: concatenated text of all dropped messages
- `dropped_count`: number of messages dropped

The mem9-save hook subscribes to both `after_reset` and `context_dropped`, saving dropped context with a `"dropped-context"` tag so it can be recalled later.

## Token Estimation

Token counting uses a lightweight heuristic that avoids depending on a tokenizer library. ASCII/Latin text is estimated at roughly 4 characters per token (via ceiling division `(ascii+3)/4`), while CJK characters (Unified Ideographs, Hangul, Hiragana, Katakana, fullwidth forms, CJK punctuation) are each counted as one token. The estimate is intentionally conservative -- slightly over-counting -- so that `trimToFit` errs on the side of fitting rather than overflowing.

`EstimateTokens(msgs)` sums the per-string estimate over each message's `Content` and each tool call's `Arguments`.

`EstimateOverhead(systemPrompt, tools)` estimates tokens for the system prompt and all tool definitions (name + description + JSON schema parameters).

## trimToFit

`trimToFit` drops the oldest messages until `EstimateTokens(msgs) <= maxTokens`. It enforces two invariants: it always keeps at least the last message (the loop exits when `len(msgs) == 1`), and it preserves tool call/result pairs -- if the oldest message is an assistant with `ToolCalls`, it drops that message together with all immediately following `RoleTool` messages, preventing orphaned tool results that would cause API errors. If dropping the pair would empty the slice, the loop stops. Orphaned `RoleTool` messages at the front (left over from a prior trim) are dropped individually.

## Session Exit Coverage

All session exit paths now produce summaries and fire the `after_reset` hook for mem9 persistence:

| Exit Path | Summarizes | Fires `after_reset` | Notes |
|-----------|:----------:|:-------------------:|-------|
| `:reset` / `/new` | Yes | Yes | Manual reset via `SessionManager.Reset` |
| Idle eviction | Yes | Yes | `EvictIdle` summarizes + hooks outside the lock |
| Capacity eviction | Yes | Yes | `evictOldestLocked` returns session; caller summarizes outside the lock |
| Shutdown | Yes | Yes | `Shutdown` collects all sessions, summarizes in parallel (30s timeout) |

## Current Gaps

1. **No semantic/relevance-based selection.** All strategies use a positional window (everything after the last anchor). There is no mechanism to keep semantically important older messages while dropping less relevant recent ones.
2. **Token estimation is coarse.** The heuristic ignores subword tokenization, special tokens, and message framing overhead. Overhead budgeting helps but is still approximate.
3. **No per-strategy configuration surface.** `MaskThreshold`, `SummarizeThreshold`, and `MaxOutputChars` are hardcoded in `NewContextStrategy`. There is no way to tune them via config or env vars.
4. **Summary injection is strategy-agnostic.** The summary-prepend logic lives in `tape.BuildMessages`, not in the strategy. A strategy cannot opt out of or customize how summaries are incorporated.
