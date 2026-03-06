package hooks

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MetricsHook collects operational metrics from agent lifecycle events.
type MetricsHook struct {
	mu sync.Mutex

	// LLM call metrics.
	llmCalls         atomic.Int64
	llmErrors        atomic.Int64
	totalPromptTok   atomic.Int64
	totalCompleteTok atomic.Int64

	// Tool metrics.
	toolCalls  map[string]int64
	toolErrors map[string]int64

	// Timing.
	llmCallStart time.Time // set in before_llm_call, read in after_llm_call
	llmLatencyMs []int64   // ring buffer of recent latencies
}

// NewMetricsHook creates a MetricsHook.
func NewMetricsHook() *MetricsHook {
	return &MetricsHook{
		toolCalls:  make(map[string]int64),
		toolErrors: make(map[string]int64),
	}
}

func (h *MetricsHook) Name() string { return "metrics" }

func (h *MetricsHook) Handle(_ context.Context, event Event) error {
	switch event.Type {
	case EventBeforeLLMCall:
		h.mu.Lock()
		h.llmCallStart = time.Now()
		h.mu.Unlock()

	case EventAfterLLMCall:
		h.llmCalls.Add(1)
		if pt, ok := event.Payload["prompt_tokens"].(int); ok {
			h.totalPromptTok.Add(int64(pt))
		}
		if ct, ok := event.Payload["completion_tokens"].(int); ok {
			h.totalCompleteTok.Add(int64(ct))
		}
		h.mu.Lock()
		if !h.llmCallStart.IsZero() {
			ms := time.Since(h.llmCallStart).Milliseconds()
			if len(h.llmLatencyMs) >= 100 {
				h.llmLatencyMs = h.llmLatencyMs[1:]
			}
			h.llmLatencyMs = append(h.llmLatencyMs, ms)
			h.llmCallStart = time.Time{}
		}
		h.mu.Unlock()

	case EventAfterToolExec:
		name, _ := event.Payload["tool_name"].(string)
		result, _ := event.Payload["result"].(string)
		h.mu.Lock()
		h.toolCalls[name]++
		if strings.HasPrefix(result, "Error:") || strings.HasPrefix(result, "Tool execution blocked") {
			h.toolErrors[name]++
		}
		h.mu.Unlock()

	case EventError:
		h.llmErrors.Add(1)
	}
	return nil
}

// Snapshot returns a point-in-time copy of all metrics for external consumption.
type MetricsSnapshot struct {
	LLMCalls         int64
	LLMErrors        int64
	TotalPromptTok   int64
	TotalCompleteTok int64
	LLMLatencyMs     []int64
	ToolCalls        map[string]int64
	ToolErrors       map[string]int64
}

// Snapshot returns an atomic snapshot of all collected metrics.
func (h *MetricsHook) Snapshot() MetricsSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()

	tc := make(map[string]int64, len(h.toolCalls))
	maps.Copy(tc, h.toolCalls)
	te := make(map[string]int64, len(h.toolErrors))
	maps.Copy(te, h.toolErrors)
	lat := slices.Clone(h.llmLatencyMs)

	return MetricsSnapshot{
		LLMCalls:         h.llmCalls.Load(),
		LLMErrors:        h.llmErrors.Load(),
		TotalPromptTok:   h.totalPromptTok.Load(),
		TotalCompleteTok: h.totalCompleteTok.Load(),
		LLMLatencyMs:     lat,
		ToolCalls:        tc,
		ToolErrors:       te,
	}
}

// Summary returns a formatted string of collected metrics.
func (h *MetricsHook) Summary() string {
	h.mu.Lock()
	defer h.mu.Unlock()

	var b strings.Builder
	fmt.Fprintf(&b, "LLM calls: %d (errors: %d)\n", h.llmCalls.Load(), h.llmErrors.Load())
	fmt.Fprintf(&b, "Tokens: prompt=%d completion=%d total=%d\n",
		h.totalPromptTok.Load(), h.totalCompleteTok.Load(),
		h.totalPromptTok.Load()+h.totalCompleteTok.Load())

	if len(h.llmLatencyMs) > 0 {
		var sum int64
		var min, max int64 = h.llmLatencyMs[0], h.llmLatencyMs[0]
		for _, ms := range h.llmLatencyMs {
			sum += ms
			if ms < min {
				min = ms
			}
			if ms > max {
				max = ms
			}
		}
		avg := sum / int64(len(h.llmLatencyMs))
		fmt.Fprintf(&b, "LLM latency (last %d): avg=%dms min=%dms max=%dms\n",
			len(h.llmLatencyMs), avg, min, max)
	}

	if len(h.toolCalls) > 0 {
		b.WriteString("Tool calls:\n")
		for name, count := range h.toolCalls {
			errs := h.toolErrors[name]
			if errs > 0 {
				fmt.Fprintf(&b, "  %-20s %d (%d errors)\n", name, count, errs)
			} else {
				fmt.Fprintf(&b, "  %-20s %d\n", name, count)
			}
		}
	}

	return b.String()
}
