package ctxmgr

import (
	"context"
	"fmt"
	"strings"

	"github.com/joechenrh/golem/internal/llm"
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

// NewContextStrategy creates a strategy from a config name.
func NewContextStrategy(name string) (ContextStrategy, error) {
	switch name {
	case "anchor":
		return &AnchorStrategy{}, nil
	case "masking":
		return &MaskingStrategy{MaskThreshold: 0.5, MaxOutputChars: 2000}, nil
	default:
		return nil, fmt.Errorf("unknown context strategy: %q", name)
	}
}

// AnchorStrategy sends all messages since the last anchor, verbatim.
// If context exceeds maxTokens, oldest messages are dropped.
type AnchorStrategy struct{}

func (s *AnchorStrategy) Name() string { return "anchor" }

func (s *AnchorStrategy) BuildContext(
	_ context.Context, entries []tape.TapeEntry,
	maxTokens int,
) ([]llm.Message, error) {
	msgs := tape.BuildMessages(entries)
	return trimToFit(msgs, maxTokens), nil
}

// MaskingStrategy extends AnchorStrategy by truncating large tool outputs
// when total tokens exceed a threshold.
type MaskingStrategy struct {
	MaskThreshold  float64 // fraction of maxTokens before masking kicks in (default: 0.5)
	MaxOutputChars int     // max chars per tool output before truncation (default: 2000)
}

func (s *MaskingStrategy) Name() string { return "masking" }

func (s *MaskingStrategy) BuildContext(
	_ context.Context, entries []tape.TapeEntry,
	maxTokens int,
) ([]llm.Message, error) {
	msgs := tape.BuildMessages(entries)

	threshold := int(float64(maxTokens) * s.MaskThreshold)
	if EstimateTokens(msgs) > threshold {
		msgs = MaskObservations(msgs, s.MaxOutputChars)
	}

	return trimToFit(msgs, maxTokens), nil
}

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
		if isCJK(r) {
			cjk++
		} else {
			ascii++
		}
	}
	return (ascii+3)/4 + cjk
}

// isCJK returns true for CJK Unified Ideographs and common CJK ranges.
func isCJK(r rune) bool {
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
