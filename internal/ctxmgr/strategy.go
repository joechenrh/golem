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

func (s *AnchorStrategy) BuildContext(_ context.Context, entries []tape.TapeEntry, maxTokens int) ([]llm.Message, error) {
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

func (s *MaskingStrategy) BuildContext(_ context.Context, entries []tape.TapeEntry, maxTokens int) ([]llm.Message, error) {
	msgs := tape.BuildMessages(entries)

	threshold := int(float64(maxTokens) * s.MaskThreshold)
	if EstimateTokens(msgs) > threshold {
		msgs = MaskObservations(msgs, s.MaxOutputChars)
	}

	return trimToFit(msgs, maxTokens), nil
}

// EstimateTokens roughly estimates token count using ~4 chars per token.
func EstimateTokens(msgs []llm.Message) int {
	total := 0
	for _, m := range msgs {
		total += len(m.Content)
		for _, tc := range m.ToolCalls {
			total += len(tc.Arguments)
		}
	}
	return total / 4
}

// MaskObservations truncates tool result messages exceeding maxChars.
// Preserves the first and last portions with a truncation marker.
func MaskObservations(msgs []llm.Message, maxChars int) []llm.Message {
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
// Always keeps at least the last message.
func trimToFit(msgs []llm.Message, maxTokens int) []llm.Message {
	for len(msgs) > 1 && EstimateTokens(msgs) > maxTokens {
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
