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

// hookPayload is the unified JSON envelope sent to all external hooks.
type hookPayload struct {
	Event     EventType      `json:"event"`
	AgentName string         `json:"agent_name"`
	Data      map[string]any `json:"data"`
}

// hookResult is the expected JSON output from a hook.
type hookResult struct {
	Content string `json:"content"`
}

// Run executes all hooks subscribed to the given event.
// For blocking events (before_*), it returns concatenated injected content.
// For non-blocking events, it returns ("", nil) — errors are logged, not returned.
func (r *Runner) Run(ctx context.Context, event string, agentName string, data map[string]any) (string, error) {
	et := EventType(event)
	payload := hookPayload{Event: et, AgentName: agentName, Data: data}

	encoded, err := json.Marshal(payload)
	if err != nil {
		if et.IsBlocking() {
			return "", fmt.Errorf("marshal %s payload: %w", event, err)
		}
		r.logger.Warn("marshal external hook payload failed",
			zap.String("event", event), zap.Error(err))
		return "", nil
	}

	var parts []string
	for _, h := range r.hooks {
		if !h.subscribedTo(et) {
			continue
		}

		r.logger.Debug("running external hook",
			zap.String("hook", h.Name), zap.String("event", event))
		start := time.Now()
		out, err := r.executeHook(ctx, h, encoded)
		elapsed := time.Since(start)
		if err != nil {
			r.logger.Warn("external hook failed",
				zap.String("hook", h.Name),
				zap.String("event", event),
				zap.Duration("elapsed", elapsed),
				zap.Error(err))
			continue
		}

		if et.IsBlocking() {
			var result hookResult
			if err := json.Unmarshal(out, &result); err != nil {
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
		} else {
			r.logger.Debug("external hook completed",
				zap.String("hook", h.Name),
				zap.String("event", event),
				zap.Duration("elapsed", elapsed))
		}
	}

	return strings.Join(parts, "\n"), nil
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
