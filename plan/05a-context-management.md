# Step 5a: Context Window Management Strategy

## The Problem

The append-only tape stores all conversation history, but LLMs have finite context windows (128K-200K tokens). When history grows large, we must decide what to send to the LLM.

## How Others Handle This

| Product | Strategy | Details |
|---|---|---|
| **Claude Code** | Auto-compaction | At ~80% context, server-side summarization compresses old messages. Clears tool outputs first, then summarizes. |
| **Cursor** | Manual curation + RAG | User selects context via `@` references. Vector search retrieves relevant code. |
| **OpenAI Agents SDK** | Trimming or summarization | Option 1: Keep last N turns. Option 2: Compress old turns into summaries. |
| **LangGraph** | Dual-layer memory | Short-term (thread checkpoints) + long-term (namespaced cross-session store). Compress/select/isolate pattern. |
| **crabclaw/bub** | Tape + anchors | Anchors mark boundaries. Only messages since last anchor are sent. Full history stays on disk. |

### Key Research Findings

1. **Context rot**: All LLMs degrade with longer inputs. Performance drops 30%+ when relevant info is in the "middle" (lost-in-the-middle effect).
2. **Observation masking** (truncating verbose tool outputs) halves cost and matches summarization quality.
3. **Summarization** costs ~7% of token budget but preserves long-range recall.
4. **Sliding window** is cheapest but creates abrupt information loss.

---

## Design: ContextStrategy Interface

Context management is abstracted as a **pluggable strategy** — the agent loop doesn't know how context is compressed, only that it calls `strategy.BuildContext()` before each LLM call.

### File

`internal/ctxmgr/strategy.go` (new package — named `ctxmgr` to avoid collision with stdlib `context`)

### Interface

```go
// internal/ctxmgr/strategy.go
package ctxmgr

// ContextStrategy determines how conversation history is assembled for an LLM call.
// The tape stores everything; the strategy decides what subset to send.
type ContextStrategy interface {
    // BuildContext assembles messages for the LLM from tape entries.
    // maxTokens is the model's context window size.
    // Returns the messages to send and any error.
    BuildContext(ctx context.Context, entries []tape.TapeEntry, maxTokens int) ([]llm.Message, error)

    // Name returns the strategy name (for logging/config).
    Name() string
}
```

### Built-in Strategies

#### 1. `AnchorStrategy` (simplest, default for Phase 1)

```go
// AnchorStrategy sends all messages since the last anchor, verbatim.
// If there's no anchor, sends all messages.
// No compression, no masking — just a hard boundary.
type AnchorStrategy struct{}
```

- Equivalent to what crabclaw does today
- No LLM call overhead
- Fails gracefully: if context exceeds limit, oldest messages are dropped with a notice

#### 2. `MaskingStrategy` (observation masking)

```go
// MaskingStrategy extends AnchorStrategy by truncating large tool outputs
// when total tokens exceed a threshold.
type MaskingStrategy struct {
    MaskThreshold float64  // e.g. 0.5 = mask when >50% of context used
    MaxOutputChars int     // max chars per tool output before masking (default: 2000)
}
```

- Applies observation masking on tool result messages
- Preserves first/last N chars + `[...truncated N chars...]`
- No LLM call — purely mechanical truncation
- Research shows this alone halves cost and matches summarization quality

#### 3. `HybridStrategy` (full three-layer)

```go
// HybridStrategy combines anchor boundaries, observation masking, and
// LLM-powered summarization when context grows large.
type HybridStrategy struct {
    LLM            llm.Client  // for summarization calls
    MaskThreshold  float64     // mask when > this fraction of context (default: 0.5)
    SummarizeThreshold float64 // summarize when > this fraction (default: 0.8)
    MaxOutputChars int         // max chars per tool output (default: 2000)
}
```

Three layers applied in order:
1. **Anchor boundary** → only messages since last anchor
2. **Observation masking** → truncate verbose tool outputs (when >50% context)
3. **Summarization** → compress oldest 50% of messages (when >80% context)

The summary is stored back as a tape anchor for future reference.

#### 4. Future: `RAGStrategy` (planned, not in Phase 1-2)

Would use mnemos vector search to retrieve relevant past context instead of relying on recency alone.

### Configuration

```go
// In config.go
type Config struct {
    // ...
    ContextStrategy string // "anchor", "masking", "hybrid" (default: "masking")
    // ...
}
```

```bash
# .env
GOLEM_CONTEXT_STRATEGY=masking
```

### Agent Loop Integration

```go
// In agent.go — before each LLM call
func (a *AgentLoop) buildMessages(ctx context.Context) ([]llm.Message, error) {
    entries, _ := a.tape.EntriesSince(lastAnchor)
    maxTokens := getModelContextWindow(a.config.Model)
    return a.contextStrategy.BuildContext(ctx, entries, maxTokens)
}
```

The agent loop doesn't know which strategy is active — it just calls `BuildContext()`.

### Strategy Factory

```go
// NewContextStrategy creates a strategy from config name.
func NewContextStrategy(name string, llmClient llm.Client) (ContextStrategy, error) {
    switch name {
    case "anchor":
        return &AnchorStrategy{}, nil
    case "masking":
        return &MaskingStrategy{MaskThreshold: 0.5, MaxOutputChars: 2000}, nil
    case "hybrid":
        return &HybridStrategy{LLM: llmClient, MaskThreshold: 0.5, SummarizeThreshold: 0.8, MaxOutputChars: 2000}, nil
    default:
        return nil, fmt.Errorf("unknown context strategy: %s", name)
    }
}
```

---

## Shared Utilities

```go
// EstimateTokens roughly estimates token count for messages.
// Uses ~4 chars per token heuristic (good enough for threshold decisions).
func EstimateTokens(messages []llm.Message) int

// MaskObservations truncates tool result messages exceeding maxChars.
// Preserves first/last portion with a "[...truncated...]" marker.
func MaskObservations(messages []llm.Message, maxChars int) []llm.Message

// SummarizeMessages asks the LLM to compress messages into a brief summary.
func SummarizeMessages(ctx context.Context, client llm.Client, messages []llm.Message) (string, error)

// BuildMessagesFromEntries converts tape entries to llm.Message slice.
func BuildMessagesFromEntries(entries []tape.TapeEntry) []llm.Message
```

---

## Context Window Sizes (reference)

| Model | Context Window |
|---|---|
| GPT-4o | 128K tokens |
| GPT-4o-mini | 128K tokens |
| Claude Sonnet/Opus | 200K tokens |
| Claude Haiku | 200K tokens |

## Trade-offs by Strategy

| Strategy | Complexity | Token Cost | LLM Calls | Quality | Best For |
|---|---|---|---|---|---|
| `AnchorStrategy` | Very low | Unbounded | 0 | Perfect (until overflow) | Short sessions |
| `MaskingStrategy` | Low | ~50% reduction | 0 | Very good | Most use cases |
| `HybridStrategy` | Medium | Bounded | Occasional | Good | Long multi-tool sessions |
| `RAGStrategy` | High | Low | Per-search | Variable | Cross-session recall |

## Implementation Plan

- **Phase 1-2**: Implement `AnchorStrategy` and `MaskingStrategy`. Default to `MaskingStrategy`.
- **Phase 5+**: Implement `HybridStrategy` (needs LLM client wired in).
- **Future**: `RAGStrategy` when mnemos is integrated.

The tape itself remains append-only and immutable. The "compression" only affects what is sent to the LLM — the full history is always recoverable from the tape file.
