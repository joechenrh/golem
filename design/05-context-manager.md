# 05 — Context Manager

## Overview

The context manager (`internal/ctxmgr/`) controls which tape entries become LLM messages before each API call. The tape is an append-only JSONL log containing every message, tool result, anchor, and summary for a session. Because tapes grow without bound, a **context strategy** decides how to compress or trim that history so it fits within the model's context window.

The strategy is invoked in `session.go` inside `executeLLMCall`, where `entries` is the full tape and `maxTokens` comes from `ctxmgr.ModelContextWindow(modelName)`. `ModelContextWindow` maps model name substrings to context sizes: Claude models get 200k, GPT-4 variants get 128k, and unknown models default to 128k. The ReAct loop in `session.runReActLoop` determines `maxTokens` via this function, then `executeLLMCall` reads all tape entries and calls `BuildContext`. If there is a pending (not-yet-persisted) user message, it is appended to the resulting messages slice after strategy processing, and the final slice is sent to the LLM as `ChatRequest.Messages`.

Strategy selection is controlled by the `GOLEM_CONTEXT_STRATEGY` env var or `context-strategy` config file key (default: `"masking"`). The factory `ctxmgr.NewContextStrategy` is called in `app.go` when building the main session, in `manager.go` when creating per-chat sessions via `SessionManager`, and in `app.go` for sub-agent sessions.

## ContextStrategy Interface

```go
type ContextStrategy interface {
    BuildContext(ctx context.Context, entries []tape.TapeEntry, maxTokens int) ([]llm.Message, error)
    Name() string
}
```

`NewContextStrategy(name)` is the factory. It accepts `"anchor"` and `"masking"`.

## AnchorStrategy

The simplest strategy. It calls `tape.BuildMessages(entries)` to obtain messages, then passes the result through `trimToFit(msgs, maxTokens)`. No modification of message content occurs; what the tape recorded is what the LLM sees.

Source: `internal/tape/entry.go`

`tape.BuildMessages` scans for the last `KindAnchor` and includes only `KindMessage` entries after it. It also scans backward for the most recent `KindSummary` and, if found, injects it as a leading `[Previous conversation summary]` user message plus an assistant acknowledgment, giving restored sessions carry-forward context. User messages with a `sender_id` field get a `[sender:xxx]` prefix so the LLM can distinguish speakers in group chats.

## MaskingStrategy

Extends the anchor approach with output truncation to reclaim token budget.

| Field            | Default | Purpose                                               |
|------------------|---------|-------------------------------------------------------|
| `MaskThreshold`  | `0.5`   | Fraction of `maxTokens` above which masking activates |
| `MaxOutputChars` | `2000`  | Max chars per tool output before truncation           |

The pipeline first calls `tape.BuildMessages(entries)` (same as Anchor), then runs `EstimateTokens(msgs)`. If the estimate exceeds `maxTokens * MaskThreshold`, it calls `MaskObservations(msgs, MaxOutputChars)`. Finally, it passes the result through `trimToFit(msgs, maxTokens)`.

### MaskObservations

`MaskObservations` iterates over messages and truncates any `Role == "tool"` message whose `Content` exceeds `MaxOutputChars`. It keeps the first and last `MaxOutputChars/2` characters, replacing the middle with `\n[...truncated N chars...]\n`. Non-tool messages and short tool outputs are left untouched. The function returns a new slice; originals are not mutated.

## HybridStrategy

Config validation accepts `"hybrid"` as a valid strategy name, but `NewContextStrategy` does not yet implement it -- passing `"hybrid"` returns an error. The intent is to combine anchor-based windowing with masking, but the implementation is a current gap.

## Token Estimation

Token counting uses a lightweight heuristic that avoids depending on a tokenizer library. ASCII/Latin text is estimated at roughly 4 characters per token (via ceiling division `(ascii+3)/4`), while CJK characters (Unified Ideographs, Hangul, Hiragana, Katakana, fullwidth forms, CJK punctuation) are each counted as one token. The estimate is intentionally conservative -- slightly over-counting -- so that `trimToFit` errs on the side of fitting rather than overflowing.

`EstimateTokens(msgs)` sums the per-string estimate over each message's `Content` and each tool call's `Arguments`.

## trimToFit

`trimToFit` drops the oldest messages until `EstimateTokens(msgs) <= maxTokens`. It enforces two invariants: it always keeps at least the last message (the loop exits when `len(msgs) == 1`), and it preserves tool call/result pairs -- if the oldest message is an assistant with `ToolCalls`, it drops that message together with all immediately following `RoleTool` messages, preventing orphaned tool results that would cause API errors. If dropping the pair would empty the slice, the loop stops. Orphaned `RoleTool` messages at the front (left over from a prior trim) are dropped individually.

## Current Gaps

1. **HybridStrategy not implemented.** Config validation accepts `"hybrid"` but `NewContextStrategy` returns an error for it. The factory and config are out of sync.
2. **No semantic/relevance-based selection.** Both strategies use a positional window (everything after the last anchor). There is no mechanism to keep semantically important older messages while dropping less relevant recent ones.
3. **Token estimation is coarse.** The heuristic ignores subword tokenization, special tokens, and message framing overhead. It does not account for system prompt tokens or tool definitions, which also consume context budget.
4. **System prompt not counted.** `maxTokens` represents the full context window, but `trimToFit` only considers message tokens. The system prompt and tool schemas are sent separately and also consume context, so the effective budget for messages is smaller than `maxTokens`.
5. **No per-strategy configuration surface.** `MaskThreshold` and `MaxOutputChars` are hardcoded in `NewContextStrategy`. There is no way to tune them via config or env vars.
6. **Summary injection is strategy-agnostic.** The summary-prepend logic lives in `tape.BuildMessages`, not in the strategy. A strategy cannot opt out of or customize how summaries are incorporated.
