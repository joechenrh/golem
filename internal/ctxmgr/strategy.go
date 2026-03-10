package ctxmgr

import (
	"context"
	"fmt"
	"strings"

	"github.com/joechenrh/golem/internal/llm"
	"github.com/joechenrh/golem/internal/stringutil"
	"github.com/joechenrh/golem/internal/tape"
)

// ContextStrategy determines how conversation history is assembled for an LLM call.
type ContextStrategy interface {
	// BuildContext assembles messages for the LLM from tape entries.
	// maxTokens is the model's context window size.
	BuildContext(ctx context.Context, entries []tape.TapeEntry, maxTokens int) ([]llm.Message, error)

	// Name returns the strategy name.
	Name() string
}

// OverheadSetter is an optional interface for strategies that account for
// system prompt + tool schema overhead when computing effective context budget.
type OverheadSetter interface {
	SetOverhead(tokens int)
}

// EstimateOverhead estimates token usage for the system prompt and tool schemas
// so strategies can subtract it from the context window budget.
func EstimateOverhead(systemPrompt string, tools []llm.ToolDefinition) int {
	total := estimateStringTokens(systemPrompt)
	for _, t := range tools {
		total += estimateStringTokens(t.Name)
		total += estimateStringTokens(t.Description)
		if len(t.Parameters) > 0 {
			total += estimateStringTokens(string(t.Parameters))
		}
	}
	return total
}

// NewContextStrategy creates a strategy from a config name.
func NewContextStrategy(name string) (ContextStrategy, error) {
	switch name {
	case "anchor":
		return &AnchorStrategy{}, nil
	case "masking":
		return &MaskingStrategy{MaskThreshold: 0.3, MaxOutputChars: 1500}, nil
	case "hybrid":
		return &HybridStrategy{
			MaskThreshold:      0.3,
			SummarizeThreshold: 0.7,
			MaxOutputChars:     1500,
		}, nil
	default:
		return nil, fmt.Errorf("unknown context strategy: %q", name)
	}
}

// AnchorStrategy sends all messages since the last anchor, verbatim.
// If context exceeds maxTokens, oldest messages are dropped.
type AnchorStrategy struct {
	Overhead int // tokens consumed by system prompt + tool schemas

	// OnTrim is called when trimToFit drops messages.
	OnTrim func(droppedCount int)
}

func (s *AnchorStrategy) Name() string           { return "anchor" }
func (s *AnchorStrategy) SetOverhead(tokens int) { s.Overhead = tokens }

func (s *AnchorStrategy) BuildContext(
	_ context.Context, entries []tape.TapeEntry,
	maxTokens int,
) ([]llm.Message, error) {
	effectiveMax := maxTokens - s.Overhead
	msgs := tape.BuildMessages(entries)
	before := len(msgs)
	msgs = trimToFit(msgs, effectiveMax)
	if dropped := before - len(msgs); dropped > 0 && s.OnTrim != nil {
		s.OnTrim(dropped)
	}
	return msgs, nil
}

// MaskingStrategy extends AnchorStrategy by truncating large tool outputs
// when total tokens exceed a threshold.
type MaskingStrategy struct {
	MaskThreshold  float64 // fraction of maxTokens before masking kicks in (default: 0.5)
	MaxOutputChars int     // max chars per tool output before truncation (default: 2000)
	Overhead       int     // tokens consumed by system prompt + tool schemas

	// OnTrim is called when trimToFit drops messages. The callback receives
	// the count of dropped messages so the caller can insert an anchor.
	OnTrim func(droppedCount int)
}

func (s *MaskingStrategy) Name() string           { return "masking" }
func (s *MaskingStrategy) SetOverhead(tokens int) { s.Overhead = tokens }

func (s *MaskingStrategy) BuildContext(
	_ context.Context, entries []tape.TapeEntry,
	maxTokens int,
) ([]llm.Message, error) {
	effectiveMax := maxTokens - s.Overhead
	msgs := tape.BuildMessages(entries)

	threshold := int(float64(effectiveMax) * s.MaskThreshold)
	if EstimateTokens(msgs) > threshold {
		msgs = MaskObservations(msgs, s.MaxOutputChars)
	}

	before := len(msgs)
	msgs = trimToFit(msgs, effectiveMax)
	if dropped := before - len(msgs); dropped > 0 && s.OnTrim != nil {
		s.OnTrim(dropped)
	}
	return msgs, nil
}

// HybridStrategy combines LLM-powered summarization with adaptive masking.
// Pipeline:
//  1. If tokens > effectiveMax * SummarizeThreshold: summarize oldest half via LLM
//  2. If tokens > effectiveMax * MaskThreshold: mask large tool outputs
//  3. trimToFit as last resort (with OnDrop callback)
type HybridStrategy struct {
	MaskThreshold      float64 // fraction of effectiveMax before masking kicks in (default: 0.5)
	SummarizeThreshold float64 // fraction of effectiveMax before LLM summarization kicks in (default: 0.7)
	MaxOutputChars     int     // max chars per tool output before truncation (default: 2000)
	Overhead           int     // tokens consumed by system prompt + tool schemas

	// LLM and Model are set after construction by the wiring layer.
	// When LLM is nil, summarization is skipped (falls back to mask-only).
	LLM   llm.Client
	Model string

	// OnDrop is called with messages about to be discarded by trimToFit.
	// Wired by session.go to fire the context_dropped hook.
	OnDrop func(ctx context.Context, dropped []llm.Message)
}

func (s *HybridStrategy) Name() string           { return "hybrid" }
func (s *HybridStrategy) SetOverhead(tokens int) { s.Overhead = tokens }

func (s *HybridStrategy) BuildContext(
	ctx context.Context, entries []tape.TapeEntry,
	maxTokens int,
) ([]llm.Message, error) {
	effectiveMax := maxTokens - s.Overhead
	msgs := tape.BuildMessages(entries)

	// Step 1: LLM summarization of oldest messages when over threshold.
	sumThreshold := int(float64(effectiveMax) * s.SummarizeThreshold)
	if s.LLM != nil && EstimateTokens(msgs) > sumThreshold && len(msgs) > 4 {
		summarized, err := s.summarizeOldest(ctx, msgs)
		if err == nil {
			msgs = summarized
		}
		// On error, fall through to masking/trimming.
	}

	// Step 2: Mask large tool outputs.
	maskThreshold := int(float64(effectiveMax) * s.MaskThreshold)
	if EstimateTokens(msgs) > maskThreshold {
		msgs = MaskObservations(msgs, s.MaxOutputChars)
	}

	// Step 3: trimToFit as last resort, with OnDrop callback.
	msgs = s.trimWithCallback(ctx, msgs, effectiveMax)
	return msgs, nil
}

// summarizeOldest takes the oldest half of messages, asks the LLM to distill
// them into a concise summary, and replaces them with a synthetic message.
func (s *HybridStrategy) summarizeOldest(
	ctx context.Context, msgs []llm.Message,
) ([]llm.Message, error) {
	half := len(msgs) / 2
	if half < 2 {
		return msgs, nil
	}
	oldest := msgs[:half]
	rest := msgs[half:]

	// Build a condensed text representation of the oldest messages.
	var sb strings.Builder
	for _, m := range oldest {
		fmt.Fprintf(&sb, "[%s]: %s\n", m.Role, stringutil.Truncate(m.Content, 500))
	}

	summaryReq := llm.ChatRequest{
		Model: s.Model,
		Messages: []llm.Message{
			{
				Role: llm.RoleUser,
				Content: "Distill the following conversation into a concise summary. " +
					"Preserve: decisions made, key facts, IDs/names, pending tasks, and outcomes. " +
					"Use structured format:\n" +
					"TOPIC: <one line>\nDECISIONS: <bullet list>\nOUTCOMES: <bullet list>\n" +
					"PENDING: <bullet list>\nKEY FACTS: <bullet list>\n\n" +
					"Conversation:\n" + sb.String(),
			},
		},
		MaxTokens: 1024,
	}

	resp, err := s.LLM.Chat(ctx, summaryReq)
	if err != nil {
		return nil, fmt.Errorf("summarize oldest: %w", err)
	}

	summaryMsg := llm.Message{
		Role:    llm.RoleUser,
		Content: "[Summarized earlier context]\n" + resp.Content,
	}
	ackMsg := llm.Message{
		Role:    llm.RoleAssistant,
		Content: "Understood. I have the summarized context from earlier in our conversation.",
	}

	result := make([]llm.Message, 0, 2+len(rest))
	result = append(result, summaryMsg, ackMsg)
	result = append(result, rest...)
	return result, nil
}

// trimWithCallback is like trimToFit but calls OnDrop before discarding messages.
func (s *HybridStrategy) trimWithCallback(
	ctx context.Context, msgs []llm.Message, maxTokens int,
) []llm.Message {
	if EstimateTokens(msgs) <= maxTokens || len(msgs) <= 1 {
		return msgs
	}

	// Collect messages that will be dropped.
	var dropped []llm.Message
	for len(msgs) > 1 && EstimateTokens(msgs) > maxTokens {
		if msgs[0].Role == llm.RoleAssistant && len(msgs[0].ToolCalls) > 0 {
			i := 1
			for i < len(msgs) && msgs[i].Role == llm.RoleTool {
				i++
			}
			if i >= len(msgs) {
				break
			}
			dropped = append(dropped, msgs[:i]...)
			msgs = msgs[i:]
			continue
		}
		if msgs[0].Role == llm.RoleTool {
			dropped = append(dropped, msgs[0])
			msgs = msgs[1:]
			continue
		}
		dropped = append(dropped, msgs[0])
		msgs = msgs[1:]
	}

	if len(dropped) > 0 && s.OnDrop != nil {
		go s.OnDrop(ctx, dropped)
	}
	return msgs
}

// truncateForSummary limits a string to maxLen chars for summarization input.
// EstimateTokens roughly estimates token count.
// ASCII/Latin text uses ~4 chars per token; CJK characters are ~1 token each.
func EstimateTokens(msgs []llm.Message) int {
	total := 0
	for _, m := range msgs {
		total += estimateStringTokens(m.Content)
		for _, tc := range m.ToolCalls {
			total += estimateStringTokens(tc.Arguments)
		}
		total += len(m.Images) * 1000
	}
	return total
}

// estimateStringTokens estimates token count for a single string,
// distinguishing CJK characters (~1 token each) from Latin text (~4 chars/token).
func estimateStringTokens(s string) int {
	ascii := 0
	cjk := 0
	for _, r := range s {
		if IsCJK(r) {
			cjk++
		} else {
			ascii++
		}
	}
	return (ascii+3)/4 + cjk
}

// IsCJK returns true for CJK Unified Ideographs and common CJK ranges.
func IsCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || // CJK Unified Ideographs
		(r >= 0x3400 && r <= 0x4DBF) || // CJK Extension A
		(r >= 0xF900 && r <= 0xFAFF) || // CJK Compatibility Ideographs
		(r >= 0x3000 && r <= 0x303F) || // CJK Symbols and Punctuation
		(r >= 0xFF00 && r <= 0xFFEF) || // Fullwidth Forms
		(r >= 0xAC00 && r <= 0xD7AF) || // Hangul Syllables (Korean)
		(r >= 0x3040 && r <= 0x309F) || // Hiragana
		(r >= 0x30A0 && r <= 0x30FF) // Katakana
}

// MaskObservations truncates tool result messages exceeding maxChars.
// Preserves the first and last portions with a truncation marker.
func MaskObservations(
	msgs []llm.Message, maxChars int,
) []llm.Message {
	result := make([]llm.Message, len(msgs))
	for i, m := range msgs {
		result[i] = m
		if m.Role == llm.RoleTool && len(m.Content) > maxChars {
			keep := maxChars / 2
			truncated := len(m.Content) - maxChars
			result[i].Content = m.Content[:keep] +
				fmt.Sprintf("\n[...truncated %d chars...]\n", truncated) +
				m.Content[len(m.Content)-keep:]
		}
	}
	return result
}

// trimToFit drops oldest messages if total tokens exceed maxTokens.
// Always keeps at least the last message. Preserves tool call/result
// pairs: if the oldest message is an assistant with tool_calls, skip
// forward past the corresponding tool results to avoid orphaning them.
func trimToFit(msgs []llm.Message, maxTokens int) []llm.Message {
	for len(msgs) > 1 && EstimateTokens(msgs) > maxTokens {
		// If the first message is an assistant with tool_calls,
		// drop it AND all immediately following tool results.
		if msgs[0].Role == llm.RoleAssistant && len(msgs[0].ToolCalls) > 0 {
			i := 1
			for i < len(msgs) && msgs[i].Role == llm.RoleTool {
				i++
			}
			if i >= len(msgs) {
				break // would drop everything; keep as-is
			}
			msgs = msgs[i:]
			continue
		}
		// If the first message is an orphaned tool result, drop it.
		if msgs[0].Role == llm.RoleTool {
			msgs = msgs[1:]
			continue
		}
		msgs = msgs[1:]
	}
	return msgs
}

// ModelContextWindow returns the context window size for known models.
// Returns a conservative default for unknown models.
func ModelContextWindow(model string) int {
	lower := strings.ToLower(model)
	switch {
	case strings.Contains(lower, "gpt-5"):
		return 400_000
	case strings.Contains(lower, "claude"):
		return 200_000
	case strings.Contains(lower, "gpt-4o"):
		return 128_000
	case strings.Contains(lower, "gpt-4"):
		return 128_000
	default:
		return 128_000
	}
}
