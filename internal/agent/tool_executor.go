package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/joechenrh/golem/internal/hooks"
	"github.com/joechenrh/golem/internal/llm"
	"github.com/joechenrh/golem/internal/redact"
	"github.com/joechenrh/golem/internal/stringutil"
	"github.com/joechenrh/golem/internal/tools"
)

// ToolExecutor handles tool call orchestration: parallel execution, hook
// emission, failure tracking, and completed-task injection.
type ToolExecutor struct {
	tools     *tools.Registry
	hooks     *hooks.Bus
	tasks     *TaskTracker
	redactor  *redact.Redactor
	logger    *zap.Logger
	sessionID string
}

// ProcessToolCalls records the assistant message, expands tool schemas, and
// executes each tool call in parallel, recording results to the tape in order.
// toolFailures is mutated in place to track consecutive failures per tool.
func (te *ToolExecutor) ProcessToolCalls(
	ctx context.Context, resp *llm.ChatResponse, iter int,
	appendMessage func(role llm.Role, content string, toolCalls []llm.ToolCall, senderID string, images []llm.ImageContent),
	appendToolResult func(toolCallID, toolName, result string),
	toolFailures map[string]int,
) {
	appendMessage(llm.RoleAssistant, resp.Content, resp.ToolCalls, "", nil)

	// Auto-expand any tool the model calls, so the next iteration
	// sends the full parameter schema (progressive disclosure).
	for _, tc := range resp.ToolCalls {
		te.tools.ExpandAt(tc.Name, iter)
	}
	if resp.Content != "" {
		te.tools.ExpandHints(resp.Content)
	}

	// Execute tool calls in parallel and collect results keyed by index.
	type toolResultEntry struct {
		id     string
		name   string
		result string
	}
	var results sync.Map
	g, gctx := errgroup.WithContext(ctx)
	for i, tc := range resp.ToolCalls {
		g.Go(func() error {
			res := te.ExecuteTool(gctx, tc)
			results.Store(i, toolResultEntry{id: tc.ID, name: tc.Name, result: res})
			return nil
		})
	}
	g.Wait()

	// Append results in original tool call order and track failures.
	for i := range resp.ToolCalls {
		v, _ := results.Load(i)
		r := v.(toolResultEntry)
		appendToolResult(r.id, r.name, r.result)

		if strings.HasPrefix(r.result, "Error:") {
			toolFailures[r.name]++
			if toolFailures[r.name] >= maxToolFailures {
				appendMessage(llm.RoleUser,
					fmt.Sprintf("Tool %q has failed %d times this turn. Reconsider your approach — try a different tool or method.",
						r.name, toolFailures[r.name]), nil, "", nil)
			}
		}
	}
}

// ExecuteTool runs a single tool call with hook emission.
func (te *ToolExecutor) ExecuteTool(
	ctx context.Context, tc llm.ToolCall,
) string {
	// Before tool exec hook — can block execution.
	if err := te.hooks.Emit(ctx, hooks.Event{
		Type: hooks.EventBeforeToolExec,
		Payload: map[string]any{
			"tool_name":  tc.Name,
			"tool_id":    tc.ID,
			"arguments":  tc.Arguments,
			"session_id": te.sessionID,
		},
	}); err != nil {
		return "Tool execution blocked: " + err.Error()
	}

	ctx = tools.WithTaskTracker(ctx, te.tasks)

	start := time.Now()
	result, err := te.tools.Execute(ctx, tc.Name, tc.Arguments)
	duration := time.Since(start)
	if err != nil {
		result = "Error: " + err.Error()
	}

	te.logger.Debug("tool executed",
		zap.String("tool", tc.Name),
		zap.Duration("duration", duration),
		zap.Bool("error", err != nil))

	te.hooks.Emit(ctx, hooks.Event{
		Type: hooks.EventAfterToolExec,
		Payload: map[string]any{
			"tool_name":   tc.Name,
			"tool_id":     tc.ID,
			"result":      stringutil.Truncate(result, maxLogTruncateLen),
			"duration_ms": duration.Milliseconds(),
			"arguments":   stringutil.Truncate(tc.Arguments, maxLogTruncateLen),
			"session_id":  te.sessionID,
		},
	})

	return result
}

// InjectCompletedTasks drains finished background tasks and returns their
// results as ephemeral user messages so the LLM can see them on the next call.
func (te *ToolExecutor) InjectCompletedTasks() []llm.Message {
	completed := te.tasks.DrainCompleted()
	var msgs []llm.Message
	for _, t := range completed {
		var msg string
		if t.Status == TaskCompleted {
			msg = fmt.Sprintf("[Background task #%d completed] %q\nResult:\n%s", t.ID, t.Description, t.Result)
		} else {
			msg = fmt.Sprintf("[Background task #%d failed] %q\nError: %s", t.ID, t.Description, t.Error)
		}
		msgs = append(msgs, llm.Message{
			Role:    llm.RoleUser,
			Content: msg,
		})
	}
	return msgs
}
