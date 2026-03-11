package agent

import (
	"context"
	"encoding/json"
	"strings"

	"go.uber.org/zap"

	"github.com/joechenrh/golem/internal/ctxmgr"
	"github.com/joechenrh/golem/internal/hooks"
	"github.com/joechenrh/golem/internal/llm"
	"github.com/joechenrh/golem/internal/router"
	"github.com/joechenrh/golem/internal/stringutil"
	"github.com/joechenrh/golem/internal/tape"
)

// runReActLoop executes the tool-calling loop until the LLM produces a final answer
// or the iteration limit is reached. If pendingMsg is non-nil, the user message
// is included in context but only persisted to the tape after the first
// successful LLM call, so a failed API request doesn't leave a dangling entry.
func (s *Session) runReActLoop(
	ctx context.Context, stream bool,
	tokenCh chan<- string,
	pendingMsg *IncomingMessage,
) (string, error) {
	s.prompt.MaybeReloadSkills()

	// Fork tape for transactional writes — entries are buffered in memory
	// until the turn completes. If the turn fails before any entries are
	// written (e.g., first LLM call errors), pending entries are discarded,
	// preventing partial entries from corrupting conversation context.
	forked := tape.Fork(s.tape)
	origTape := s.tape
	s.tape = forked
	defer func() {
		s.tape = origTape
		if forked.Pending() > 0 {
			if err := forked.Commit(); err != nil {
				s.logger.Error("tape commit failed", zap.Error(err))
			}
		}
	}()

	_, modelName := llm.ParseModelProvider(s.config.Model)
	maxTokens := ctxmgr.ModelContextWindow(modelName)

	// Reset per-turn tracking.
	s.turnUsage = llm.Usage{}
	s.toolFailures = make(map[string]int)

	// Cache system prompt once per turn to avoid rebuilding (and re-reading
	// persona files) on every LLM iteration within the same turn.
	s.cachedSystemPrompt = s.prompt.Build()
	s.prompt.SetCachedPrompt(s.cachedSystemPrompt)

	// Wire auto-anchor: when the context strategy trims messages, insert
	// an anchor so future BuildMessages calls skip the dropped region.
	s.wireAutoAnchor()

	// Expand $skill hints from the user message: inject matched skill
	// bodies into the system prompt and auto-expand referenced tools.
	if pendingMsg != nil {
		s.prompt.ExpandSkillHints(pendingMsg.Text)
		s.cachedSystemPrompt = s.prompt.CachedPrompt()
	}

	nudges := 0
	stuckEscalated := false
	s.lastTaskSummary = ""
	emptyRetries := 0

	iter := 0
	for iter < s.config.MaxToolIter {
		// Inject completed background task results as ephemeral messages.
		s.ephemeralMessages = append(s.ephemeralMessages, s.toolExec.InjectCompletedTasks()...)

		// Shrink tool schemas not used in the last few iterations to
		// save context window space in long multi-step chains.
		s.tools.ShrinkUnused(iter, shrinkAfterIters)

		resp, err := s.executeLLMCall(ctx, modelName, maxTokens, iter, stream, nil, pendingMsg, emptyRetries > 0)
		if err != nil {
			return "", err
		}
		iter++

		// First successful LLM call — persist the pending user message.
		if pendingMsg != nil {
			s.persistPendingUserMessage(ctx, pendingMsg)
			pendingMsg = nil
		}

		// Tool calls present — execute them and continue the loop.
		// Pass iter-1 because iter was already incremented after executeLLMCall.
		if len(resp.ToolCalls) > 0 {
			s.toolExec.ProcessToolCalls(ctx, resp, iter-1, s.appendMessage, s.appendToolResult, s.toolFailures)

			// If spawn_agent successfully launched a background task, nudge
			// the LLM to report status and stop making its own tool calls
			// so it reaches the task-wait phase before exhausting MaxToolIter.
			// Only inject the hint when a task is actually running — a failed
			// spawn (e.g., missing prompt) should not stop the LLM from working.
			if s.tasks.HasRunning() {
				for _, tc := range resp.ToolCalls {
					if tc.Name == "spawn_agent" {
						s.ephemeralMessages = append(s.ephemeralMessages, llm.Message{
							Role:    llm.RoleUser,
							Content: "Sub-agent(s) launched. Tell the user what you dispatched and STOP making tool calls. You will receive results automatically.",
						})
						break
					}
				}
			}
			continue
		}

		// Empty response with no tool calls — retry up to maxEmptyRetries,
		// then inject an ephemeral recovery hint to break the loop.
		if strings.TrimSpace(resp.Content) == "" {
			emptyRetries++
			s.logger.Warn("LLM returned empty response, retrying",
				zap.Int("iter", iter), zap.Int("empty_retries", emptyRetries))
			if emptyRetries >= maxEmptyRetries {
				s.ephemeralMessages = append(s.ephemeralMessages,
					llm.Message{Role: llm.RoleAssistant, Content: resp.Content},
					llm.Message{Role: llm.RoleUser, Content: emptyResponseHint(false)},
				)
				emptyRetries = 0
			}
			continue
		}
		emptyRetries = 0

		// Classifier-only nudge decision.
		shouldContinue, classifierAccepted := s.handleClassifierNudge(ctx, resp, iter, &nudges)
		if shouldContinue {
			continue
		}

		// Stuck escalation: if classifier nudged at least once and the
		// LLM still returned text-only, inject a task-specific reminder.
		if !classifierAccepted && nudges >= 1 && !stuckEscalated {
			stuckEscalated = true
			summary := s.lastTaskSummary
			if summary == "" {
				summary = sanitizeTaskSummary(s.lastUserMessage())
			}
			if summary != "" {
				s.ephemeralMessages = append(s.ephemeralMessages,
					llm.Message{Role: llm.RoleAssistant, Content: resp.Content},
					llm.Message{Role: llm.RoleUser, Content: taskReminderMessage(summary, resp.Content)},
				)
				nudges++
				s.logger.Debug("injecting task reminder (stuck escalation)",
					zap.Int("nudge", nudges), zap.Int("iter", iter),
					zap.String("task_summary", summary),
					zap.String("discarded", stringutil.Truncate(resp.Content, 200)))
				continue
			}
		}

		// Task wait: if background tasks are still running, send a status
		// summary to the user and wait in-memory for completion instead
		// of returning. This avoids burning iterations on polling.
		if s.tasks.HasRunning() {
			s.logger.Debug("waiting for background tasks before returning",
				zap.Int("iter", iter))
			if stream && tokenCh != nil {
				tokenCh <- s.tasks.Summary()
			}
			s.tasks.WaitForAny(ctx)
			s.ephemeralMessages = append(s.ephemeralMessages, s.toolExec.InjectCompletedTasks()...)
			// Don't count the wait as an iteration — the LLM wasn't called.
			continue
		}

		// Final answer — no tool calls, no running tasks.
		content := s.processAssistantResponse(ctx, resp)
		s.persistExpandedTools()
		if stream && tokenCh != nil {
			tokenCh <- content
		}
		return content, nil
	}

	// If background tasks are still running when the iteration limit is
	// reached, wait for them and give the LLM a small extra budget to
	// incorporate the results and produce a final answer.
	if s.tasks.HasRunning() {
		s.logger.Info("iteration limit reached with running tasks, entering recovery",
			zap.Int("iter", iter))
		if stream && tokenCh != nil {
			tokenCh <- s.tasks.Summary()
		}
		s.tasks.WaitForAny(ctx)
		s.ephemeralMessages = append(s.ephemeralMessages, s.toolExec.InjectCompletedTasks()...)

		for range taskRecoveryIters {
			resp, err := s.executeLLMCall(ctx, modelName, maxTokens, iter, stream, nil, nil, false)
			if err != nil {
				break
			}
			iter++

			if len(resp.ToolCalls) > 0 {
				s.toolExec.ProcessToolCalls(ctx, resp, iter-1, s.appendMessage, s.appendToolResult, s.toolFailures)
				continue
			}
			if strings.TrimSpace(resp.Content) != "" {
				content := s.processAssistantResponse(ctx, resp)
				s.persistExpandedTools()
				if stream && tokenCh != nil {
					tokenCh <- content
				}
				return content, nil
			}
		}
	}

	fallback := "Tool calling limit reached. Please try a simpler request."
	if stream && tokenCh != nil {
		tokenCh <- fallback
	}
	return fallback, nil
}

// persistPendingUserMessage records the user message to the tape and fires hooks.
func (s *Session) persistPendingUserMessage(ctx context.Context, msg *IncomingMessage) {
	var msgImages []llm.ImageContent
	for _, img := range msg.Images {
		msgImages = append(msgImages, llm.ImageContent{
			Base64:    img.Base64,
			MediaType: img.MediaType,
		})
	}
	s.appendMessage(llm.RoleUser, msg.Text, nil, msg.SenderID, msgImages)
	s.hooks.Emit(ctx, hooks.Event{
		Type:    hooks.EventUserMessage,
		Payload: map[string]any{"text": msg.Text, "channel_id": msg.ChannelID, "session_id": s.sessionID},
	})
	if s.extHooks != nil {
		// TODO: consider making fire-and-forget hooks async (goroutine) to avoid blocking the agent loop.
		s.extHooks.Run(ctx, "user_message", s.config.AgentName, map[string]any{
			"text":       msg.Text,
			"channel_id": msg.ChannelID,
		})
	}
}

// handleClassifierNudge runs the classifier to decide if the response should be
// nudged, accepted, or treated as stuck. Returns (shouldContinue, classifierAccepted).
func (s *Session) handleClassifierNudge(
	ctx context.Context, resp *llm.ChatResponse, iter int, nudges *int,
) (bool, bool) {
	if *nudges >= maxNudges {
		return false, false
	}

	lastUserMsg := s.lastUserMessage()
	toolNames := s.tools.Names()
	decision, taskSummary, ok := s.classifier.Classify(
		ctx, resp, s.tape, toolNames, lastUserMsg,
	)
	if !ok {
		return false, false
	}

	switch decision {
	case "nudge":
		s.ephemeralMessages = append(s.ephemeralMessages,
			llm.Message{Role: llm.RoleAssistant, Content: resp.Content},
			llm.Message{Role: llm.RoleUser, Content: nudgeMessage(resp.Content)},
		)
		*nudges++
		s.logger.Debug("classifier nudge",
			zap.Int("iter", iter),
			zap.String("discarded", stringutil.Truncate(resp.Content, 200)))
		return true, false
	case "stuck":
		s.lastTaskSummary = taskSummary
		s.ephemeralMessages = append(s.ephemeralMessages,
			llm.Message{Role: llm.RoleAssistant, Content: resp.Content},
			llm.Message{Role: llm.RoleUser, Content: taskReminderMessage(taskSummary, resp.Content)},
		)
		*nudges++
		s.logger.Debug("classifier stuck",
			zap.Int("iter", iter),
			zap.String("task_summary", taskSummary),
			zap.String("discarded", stringutil.Truncate(resp.Content, 200)))
		return true, false
	case "accept":
		s.logger.Debug("classifier accepted response", zap.Int("iter", iter))
		return false, true
	}
	return false, false
}

// processAssistantResponse handles the final answer: runs any embedded colon-
// commands, records the response to the tape, and returns the content.
func (s *Session) processAssistantResponse(
	ctx context.Context, resp *llm.ChatResponse,
) string {
	content := resp.Content

	commands, cleanText := router.RouteAssistant(content)
	if len(commands) > 0 {
		content = cleanText
		for _, cmd := range commands {
			route := router.RouteResult{
				IsCommand: true,
				Command:   cmd.Command,
				Args:      cmd.Args,
				Kind:      cmd.Kind,
			}
			cmdResult, _ := s.handleCommand(ctx, route)
			content += "\n" + cmdResult
		}
	}

	s.appendMessage(llm.RoleAssistant, content, nil, "", nil)
	return content
}

// lastUserMessage returns the most recent user message text from the tape.
func (s *Session) lastUserMessage() string {
	entries, err := s.tape.Entries()
	if err != nil {
		return ""
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind != tape.KindMessage {
			continue
		}
		var msg struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if json.Unmarshal(entries[i].Payload, &msg) == nil && msg.Role == "user" {
			return msg.Content
		}
	}
	return ""
}
