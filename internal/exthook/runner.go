package exthook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"go.uber.org/zap"
)

// Runner manages discovered external hooks and executes them at lifecycle points.
type Runner struct {
	hooks  []*HookDef
	logger *zap.Logger
}

// NewRunner creates a Runner from discovered hook definitions.
func NewRunner(hooks []*HookDef, logger *zap.Logger) *Runner {
	return &Runner{hooks: hooks, logger: logger}
}

// beforeLLMCallPayload is the JSON sent to hooks subscribed to before_llm_call.
type beforeLLMCallPayload struct {
	Event       EventType `json:"event"`
	AgentName   string    `json:"agent_name"`
	UserMessage string    `json:"user_message"`
	Iteration   int       `json:"iteration"`
}

// afterResetPayload is the JSON sent to hooks subscribed to after_reset.
type afterResetPayload struct {
	Event     EventType `json:"event"`
	AgentName string    `json:"agent_name"`
	Summary   string    `json:"summary"`
}

// hookResult is the expected JSON output from a hook.
type hookResult struct {
	Content string `json:"content"`
}

// BeforeLLMCall executes all hooks subscribed to before_llm_call.
// Returns concatenated injected content (empty string if no hooks or no content).
func (r *Runner) BeforeLLMCall(ctx context.Context, agentName, userMessage string, iteration int) (string, error) {
	payload := beforeLLMCallPayload{
		Event:       EventBeforeLLMCall,
		AgentName:   agentName,
		UserMessage: userMessage,
		Iteration:   iteration,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal before_llm_call payload: %w", err)
	}

	var parts []string
	for _, h := range r.hooks {
		if !h.subscribedTo(EventBeforeLLMCall) {
			continue
		}

		r.logger.Debug("running external hook",
			zap.String("hook", h.Name), zap.String("event", string(EventBeforeLLMCall)))
		start := time.Now()
		out, err := r.executeHook(ctx, h, data)
		elapsed := time.Since(start)
		if err != nil {
			r.logger.Warn("external hook failed",
				zap.String("hook", h.Name),
				zap.Duration("elapsed", elapsed),
				zap.Error(err))
			continue
		}

		var result hookResult
		if err := json.Unmarshal(out, &result); err != nil {
			// Non-JSON output or empty — skip.
			r.logger.Debug("external hook returned non-JSON output",
				zap.String("hook", h.Name), zap.String("output", string(out)))
			continue
		}

		if result.Content != "" {
			r.logger.Debug("external hook injected context",
				zap.String("hook", h.Name),
				zap.Int("content_len", len(result.Content)),
				zap.Duration("elapsed", elapsed))
			parts = append(parts, result.Content)
		}
	}

	return strings.Join(parts, "\n"), nil
}

// AfterReset executes all hooks subscribed to after_reset.
// Fire-and-forget: errors are logged, not returned.
func (r *Runner) AfterReset(ctx context.Context, summary, agentName string) {
	payload := afterResetPayload{
		Event:     EventAfterReset,
		AgentName: agentName,
		Summary:   summary,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		r.logger.Warn("marshal after_reset payload failed", zap.Error(err))
		return
	}

	for _, h := range r.hooks {
		if !h.subscribedTo(EventAfterReset) {
			continue
		}
		r.logger.Debug("running external hook",
			zap.String("hook", h.Name), zap.String("event", string(EventAfterReset)))
		start := time.Now()
		if _, err := r.executeHook(ctx, h, data); err != nil {
			r.logger.Warn("external hook after_reset failed",
				zap.String("hook", h.Name),
				zap.Duration("elapsed", time.Since(start)),
				zap.Error(err))
		} else {
			r.logger.Debug("external hook after_reset completed",
				zap.String("hook", h.Name),
				zap.Duration("elapsed", time.Since(start)))
		}
	}
}

// executeHook runs a single hook command with the given stdin data.
func (r *Runner) executeHook(ctx context.Context, h *HookDef, stdinData []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, h.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, h.Command)
	cmd.Dir = h.Dir
	cmd.Stdin = bytes.NewReader(stdinData)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			r.logger.Warn("external hook stderr",
				zap.String("hook", h.Name), zap.String("stderr", stderr.String()))
		}
		return nil, fmt.Errorf("hook %q: %w", h.Name, err)
	}

	if stderr.Len() > 0 {
		r.logger.Warn("external hook stderr",
			zap.String("hook", h.Name), zap.String("stderr", stderr.String()))
	}

	return bytes.TrimSpace(stdout.Bytes()), nil
}

// subscribedTo checks if the hook is subscribed to the given event.
func (h *HookDef) subscribedTo(event EventType) bool {
	for _, e := range h.Events {
		if e == event {
			return true
		}
	}
	return false
}
