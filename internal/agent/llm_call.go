package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"go.uber.org/zap"

	"github.com/joechenrh/golem/internal/ctxmgr"
	"github.com/joechenrh/golem/internal/hooks"
	"github.com/joechenrh/golem/internal/llm"
	"github.com/joechenrh/golem/internal/stringutil"
)

// executeLLMCall builds context, calls the LLM (streaming or not), and emits hooks.
// If pendingMsg is non-nil, its text is appended to the context as a user
// message without persisting to the tape, so that a failed API call
// does not leave a dangling tape entry.
// When skipHooks is true, external before_llm_call hooks are skipped
// (used during empty-response retries where the context hasn't changed).
func (s *Session) executeLLMCall(
	ctx context.Context, modelName string,
	maxTokens, iter int, stream bool,
	tokenCh chan<- string,
	pendingMsg *IncomingMessage,
	skipHooks bool,
) (*llm.ChatResponse, error) {
	messages, err := s.buildMessages(ctx, maxTokens, iter, pendingMsg, skipHooks)
	if err != nil {
		return nil, err
	}

	req := s.buildLLMRequest(modelName, messages)

	s.hooks.Emit(ctx, hooks.Event{
		Type:    hooks.EventBeforeLLMCall,
		Payload: map[string]any{"iteration": iter, "message_count": len(messages), "session_id": s.sessionID},
	})

	var resp *llm.ChatResponse
	if stream {
		resp, err = s.doStreamingCall(ctx, req, tokenCh)
	} else {
		resp, err = s.llm.Chat(ctx, req)
	}
	if err != nil {
		// Invalidate chain on error; next call sends full context.
		s.chainValid = false
		s.hooks.Emit(ctx, hooks.Event{
			Type:    hooks.EventError,
			Payload: map[string]any{"error": err.Error(), "session_id": s.sessionID},
		})
		return nil, fmt.Errorf("LLM call: %w", err)
	}

	s.processLLMResponse(ctx, resp)
	return resp, nil
}

// buildMessages assembles the message context for an LLM call from tape
// entries, pending user input, ephemeral messages, and external hooks.
func (s *Session) buildMessages(
	ctx context.Context, maxTokens, iter int,
	pendingMsg *IncomingMessage,
	skipHooks bool,
) ([]llm.Message, error) {
	entries, err := s.tape.Entries()
	if err != nil {
		return nil, fmt.Errorf("reading tape: %w", err)
	}
	messages, err := s.contextStrategy.BuildContext(ctx, entries, maxTokens)
	if err != nil {
		return nil, fmt.Errorf("building context: %w", err)
	}

	// Include the not-yet-persisted user message in the context.
	if pendingMsg != nil {
		content := pendingMsg.Text
		if pendingMsg.SenderID != "" {
			content = "[sender:" + pendingMsg.SenderID + "] " + content
		}
		userMsg := llm.Message{
			Role:    llm.RoleUser,
			Content: content,
		}
		for _, img := range pendingMsg.Images {
			userMsg.Images = append(userMsg.Images, llm.ImageContent{
				Base64:    img.Base64,
				MediaType: img.MediaType,
			})
		}
		messages = append(messages, userMsg)
	}

	// Inject ephemeral messages (nudges, recovery hints) then clear them.
	if len(s.ephemeralMessages) > 0 {
		messages = append(messages, s.ephemeralMessages...)
		s.ephemeralMessages = nil
	}

	// Run external hooks for context injection (skipped during retries).
	if s.extHooks != nil && !skipHooks {
		messages = s.injectExtHookContext(ctx, messages, pendingMsg, iter)
	}

	// Budget system prompt + tool schemas into the context window.
	toolDefs := s.tools.ToolDefinitions()
	if setter, ok := s.contextStrategy.(ctxmgr.OverheadSetter); ok {
		overhead := ctxmgr.EstimateOverhead(s.cachedSystemPrompt, toolDefs)
		setter.SetOverhead(overhead)
	}

	return messages, nil
}

// injectExtHookContext runs the before_llm_call external hook and appends
// any injected context to the message list.
func (s *Session) injectExtHookContext(
	ctx context.Context, messages []llm.Message,
	pendingMsg *IncomingMessage, iter int,
) []llm.Message {
	var userText string
	if pendingMsg != nil {
		userText = pendingMsg.Text
	} else {
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == llm.RoleUser {
				userText = messages[i].Content
				break
			}
		}
	}

	// Build recent_context from the last 3 user messages for richer recall.
	var recentParts []string
	count := 0
	for i := len(messages) - 1; i >= 0 && count < 3; i-- {
		if messages[i].Role == llm.RoleUser {
			recentParts = append(recentParts, messages[i].Content)
			count++
		}
	}
	// Reverse so they're in chronological order.
	slices.Reverse(recentParts)
	recentContext := strings.Join(recentParts, "\n")

	hookData := map[string]any{
		"user_message":   userText,
		"iteration":      iter,
		"recent_context": recentContext,
		"message_count":  len(messages),
	}
	s.logger.Debug("before_llm_call hook input",
		zap.String("user_message", userText),
		zap.Int("iteration", iter),
		zap.Int("message_count", len(messages)),
		zap.Int("recent_context_len", len(recentContext)))

	injected, err := s.extHooks.Run(ctx, "before_llm_call", s.config.AgentName, hookData)
	if err != nil {
		s.logger.Warn("external hook before_llm_call failed", zap.Error(err))
	} else if injected != "" {
		s.logger.Debug("before_llm_call hook injected context",
			zap.Int("injected_len", len(injected)),
			zap.String("injected_preview", stringutil.Truncate(injected, 200)))
		messages = append(messages, llm.Message{
			Role:    llm.RoleUser,
			Content: "[External context]\n" + injected,
		})
	} else {
		s.logger.Debug("before_llm_call hook returned empty content")
	}
	return messages
}

// buildLLMRequest constructs the ChatRequest from the current session state.
func (s *Session) buildLLMRequest(modelName string, messages []llm.Message) llm.ChatRequest {
	req := llm.ChatRequest{
		Model:           modelName,
		SystemPrompt:    s.cachedSystemPrompt,
		Messages:        messages,
		Tools:           s.tools.ToolDefinitions(),
		MaxTokens:       s.config.MaxOutputTokens,
		Temperature:     s.config.Temperature,
		ReasoningEffort: s.config.ReasoningEffort,
	}

	// Responses API: truncation, store, native web search, and chaining.
	if s.config.UseResponsesAPI {
		req.Truncation = "auto"
		req.Store = s.config.ResponsesStore
		req.UseNativeWebSearch = s.config.UseNativeWebSearch

		if s.lastResponseID != "" && s.chainValid {
			req.PreviousResponseID = s.lastResponseID
			req.IncrementalInput = s.incrementalMessages
		}
	}
	return req
}

// processLLMResponse updates session state after a successful LLM call:
// Responses API chain tracking, token usage, and hook emission.
func (s *Session) processLLMResponse(ctx context.Context, resp *llm.ChatResponse) {
	// Capture response ID for Responses API chaining.
	// When Store is explicitly false, the server won't remember the response,
	// so chaining via previous_response_id is not possible.
	if s.config.UseResponsesAPI && resp.ResponseID != "" {
		s.lastResponseID = resp.ResponseID
		s.chainValid = s.config.ResponsesStore == nil || *s.config.ResponsesStore
		s.incrementalMessages = nil // reset; will accumulate new messages
	}

	// Accumulate token usage.
	s.turnUsage.Add(resp.Usage)
	s.sessionUsage.Add(resp.Usage)

	s.hooks.Emit(ctx, hooks.Event{
		Type: hooks.EventAfterLLMCall,
		Payload: map[string]any{
			"finish_reason":     resp.FinishReason,
			"tool_call_count":   len(resp.ToolCalls),
			"prompt_tokens":     resp.Usage.PromptTokens,
			"completion_tokens": resp.Usage.CompletionTokens,
			"turn_total_tokens": s.turnUsage.TotalTokens,
			"session_id":        s.sessionID,
		},
	})
	if s.extHooks != nil {
		// TODO: consider making fire-and-forget hooks async (goroutine) to avoid blocking the agent loop.
		s.extHooks.Run(ctx, "after_llm_call", s.config.AgentName, map[string]any{
			"finish_reason":     resp.FinishReason,
			"tool_call_count":   len(resp.ToolCalls),
			"prompt_tokens":     resp.Usage.PromptTokens,
			"completion_tokens": resp.Usage.CompletionTokens,
		})
	}
}

// doStreamingCall performs a streaming LLM call, sending content tokens to tokenCh,
// and returns the assembled full response.
func (s *Session) doStreamingCall(
	ctx context.Context, req llm.ChatRequest,
	tokenCh chan<- string,
) (*llm.ChatResponse, error) {
	eventCh, err := s.llm.ChatStream(ctx, req)
	if err != nil {
		return nil, err
	}

	resp := &llm.ChatResponse{}
	var contentBuf strings.Builder
	// Track in-progress tool calls by index.
	toolCallMap := make(map[string]*llm.ToolCall) // keyed by ID
	var toolCallOrder []string

	for ev := range eventCh {
		switch ev.Type {
		case llm.StreamContentDelta:
			contentBuf.WriteString(ev.Content)
			if tokenCh != nil {
				select {
				case tokenCh <- ev.Content:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}

		case llm.StreamToolCallDelta:
			if ev.ToolCall == nil {
				continue
			}
			tc := ev.ToolCall
			if tc.ID != "" {
				// New tool call starting (first delta may also carry arguments).
				toolCallMap[tc.ID] = &llm.ToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments}
				toolCallOrder = append(toolCallOrder, tc.ID)
			} else if tc.Arguments != "" && len(toolCallOrder) > 0 {
				// Append arguments to the most recent tool call.
				lastID := toolCallOrder[len(toolCallOrder)-1]
				if existing, ok := toolCallMap[lastID]; ok {
					existing.Arguments += tc.Arguments
				}
			}

		case llm.StreamError:
			return nil, ev.Error

		case llm.StreamDone:
			if ev.Usage != nil {
				resp.Usage = *ev.Usage
			}
			if ev.ResponseID != "" {
				resp.ResponseID = ev.ResponseID
			}
		}
	}

	resp.Content = contentBuf.String()
	for _, id := range toolCallOrder {
		if tc, ok := toolCallMap[id]; ok {
			tc.Arguments = llm.NormalizeArgs(tc.Arguments)
			resp.ToolCalls = append(resp.ToolCalls, *tc)
		}
	}

	if len(resp.ToolCalls) > 0 {
		resp.FinishReason = "tool_calls"
	} else {
		resp.FinishReason = "stop"
	}

	return resp, nil
}

// buildSystemPrompt delegates to the PromptBuilder collaborator.
// Kept as a Session method so existing tests that call it directly continue to work.
func (s *Session) buildSystemPrompt() string {
	return s.prompt.Build()
}
