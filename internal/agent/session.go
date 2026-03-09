package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/joechenrh/golem/internal/channel"
	"github.com/joechenrh/golem/internal/config"
	"github.com/joechenrh/golem/internal/ctxmgr"
	"github.com/joechenrh/golem/internal/hooks"
	"github.com/joechenrh/golem/internal/llm"
	"github.com/joechenrh/golem/internal/router"
	"github.com/joechenrh/golem/internal/tape"
	"github.com/joechenrh/golem/internal/tools"
)

// Session orchestrates the ReAct loop for a single conversation: LLM calls,
// tool execution, tape recording, command routing, and token tracking.
// Each conversation (CLI or remote chat) gets its own Session.
type Session struct {
	llm             llm.Client
	classifierLLM   llm.Client // lightweight model for nudge classification (nil = disabled)
	tools           *tools.Registry
	tape            tape.Store
	contextStrategy ctxmgr.ContextStrategy
	hooks           *hooks.Bus
	config          *config.Config
	logger          *zap.Logger

	// Token tracking: accumulated across the session lifetime.
	sessionUsage llm.Usage
	turnUsage    llm.Usage // reset each turn

	// Self-correction: per-tool failure counts, reset each turn.
	toolFailures map[string]int

	// MetricsSummary is an optional function that returns metrics text.
	// Set by the wiring layer when a MetricsHook is registered.
	MetricsSummary func() string

	// Skill reload: periodically re-discover skills from disk.
	skillDirs           []string
	lastSkillReload     time.Time
	skillReloadInterval time.Duration

	// External hooks runner (nil if no hooks configured).
	extHooks ExtHookRunner

	// Ephemeral messages injected into the next LLM call only (nudges,
	// recovery hints). They are not persisted to the tape.
	ephemeralMessages []llm.Message

	// Task summary from classifier, used for stuck escalation.
	lastTaskSummary string

	// Cached system prompt for the current turn, rebuilt once at the
	// start of runReActLoop and reused across iterations.
	cachedSystemPrompt string

	// Responses API chain tracking (OpenAI only).
	lastResponseID      string        // previous_response_id for chaining
	chainValid          bool          // false when chain must be rebuilt (error, reset, etc.)
	incrementalMessages []llm.Message // messages since last LLM call (for incremental input)

	// Lifecycle fields (managed by SessionManager for remote chats;
	// unused for the default CLI session).
	ctx        context.Context
	cancel     context.CancelFunc
	lastAccess time.Time
	TapePath   string
}

const (
	// max auto-nudges per user turn before accepting the response
	maxNudges = 2

	// max consecutive empty LLM responses before injecting a recovery hint
	maxEmptyRetries = 3

	// consecutive failures of a single tool before injecting a "reconsider" hint
	maxToolFailures = 3

	// compact unused tool schemas after this many iterations
	shrinkAfterIters = 10

	// max chars for log truncation
	maxLogTruncateLen = 500

	// max messages to include in summarization
	maxSummaryMessages = 80
)

// toolUseInstruction is the shared tool-use guidance included in all system prompts.
const toolUseInstruction = "When you need to perform actions, use the available tools immediately. " +
	"You may briefly explain your reasoning alongside tool calls, but always " +
	"include the tool calls in the same response — never respond with only a " +
	"plan or description of what you intend to do.\n"

// ExtHookRunner is satisfied by exthook.Runner.
// Defined here as an interface to avoid a circular import.
type ExtHookRunner interface {
	Run(ctx context.Context, event string, agentName string, data map[string]any) (string, error)
}

// SetSkillReload configures periodic skill reload from the given directories.
func (s *Session) SetSkillReload(dirs []string, interval time.Duration) {
	s.skillDirs = dirs
	s.skillReloadInterval = interval
}

// SetExtHooks sets the external hook runner for this session.
func (s *Session) SetExtHooks(runner ExtHookRunner) {
	s.extHooks = runner
}

// maybeReloadSkills re-discovers skills from disk if enough time has elapsed.
func (s *Session) maybeReloadSkills() {
	if s.skillReloadInterval <= 0 || len(s.skillDirs) == 0 {
		return
	}
	if time.Since(s.lastSkillReload) < s.skillReloadInterval {
		return
	}
	s.lastSkillReload = time.Now()
	if n := s.tools.ReloadSkills(s.skillDirs); n > 0 {
		s.logger.Info("reloaded skills from disk", zap.Int("updated", n))
	}
}

// NewSession creates a Session with all dependencies wired in.
func NewSession(
	llmClient llm.Client,
	classifierLLM llm.Client,
	toolRegistry *tools.Registry,
	tapeStore tape.Store,
	ctxStrategy ctxmgr.ContextStrategy,
	hookBus *hooks.Bus,
	cfg *config.Config,
	logger *zap.Logger,
) *Session {
	return &Session{
		llm:             llmClient,
		classifierLLM:   classifierLLM,
		tools:           toolRegistry,
		tape:            tapeStore,
		contextStrategy: ctxStrategy,
		hooks:           hookBus,
		config:          cfg,
		logger:          logger,
	}
}

// HandleInput processes a user message and returns the final response.
// Used by non-streaming channels.
func (s *Session) HandleInput(
	ctx context.Context, msg channel.IncomingMessage,
) (string, error) {
	// Inject channel ID so tools (e.g. chat_history) can access it.
	ctx = channel.WithChannelID(ctx, msg.ChannelID)

	// Route user input.
	route := router.RouteUser(msg.Text)
	if route.IsCommand {
		return s.handleCommand(ctx, route)
	}

	return s.runReActLoop(ctx, false, nil, &msg)
}

// HandleInputStream processes a user message with streaming.
// Tokens are sent to tokenCh as they arrive. Used by CLI.
func (s *Session) HandleInputStream(
	ctx context.Context, msg channel.IncomingMessage,
	tokenCh chan<- string,
) error {
	// Inject channel ID so tools (e.g. chat_history) can access it.
	ctx = channel.WithChannelID(ctx, msg.ChannelID)

	// Route user input.
	route := router.RouteUser(msg.Text)
	if route.IsCommand {
		result, err := s.handleCommand(ctx, route)
		if err != nil {
			return err
		}
		tokenCh <- result
		return nil
	}

	_, err := s.runReActLoop(ctx, true, tokenCh, &msg)
	return err
}

// runReActLoop executes the tool-calling loop until the LLM produces a final answer
// or the iteration limit is reached. If pendingMsg is non-nil, the user message
// is included in context but only persisted to the tape after the first
// successful LLM call, so a failed API request doesn't leave a dangling entry.
func (s *Session) runReActLoop(
	ctx context.Context, stream bool,
	tokenCh chan<- string,
	pendingMsg *channel.IncomingMessage,
) (string, error) {
	s.maybeReloadSkills()

	_, modelName := llm.ParseModelProvider(s.config.Model)
	maxTokens := ctxmgr.ModelContextWindow(modelName)

	// Reset per-turn tracking.
	s.turnUsage = llm.Usage{}
	s.toolFailures = make(map[string]int)

	// Cache system prompt once per turn to avoid rebuilding (and re-reading
	// persona files) on every LLM iteration within the same turn.
	s.cachedSystemPrompt = s.buildSystemPrompt()

	nudges := 0
	s.lastTaskSummary = ""
	emptyRetries := 0
	lastToolFailed := false // previous iteration had a tool failure

	for iter := range s.config.MaxToolIter {
		// Shrink tool schemas not used in the last few iterations to
		// save context window space in long multi-step chains.
		s.tools.ShrinkUnused(iter, shrinkAfterIters)

		resp, err := s.executeLLMCall(ctx, modelName, maxTokens, iter, stream, tokenCh, pendingMsg, emptyRetries > 0)
		if err != nil {
			return "", err
		}

		// First successful LLM call — persist the pending user message.
		if pendingMsg != nil {
			var msgImages []llm.ImageContent
			for _, img := range pendingMsg.Images {
				msgImages = append(msgImages, llm.ImageContent{
					Base64:    img.Base64,
					MediaType: img.MediaType,
				})
			}
			s.appendMessage(llm.RoleUser, pendingMsg.Text, nil, pendingMsg.SenderID, msgImages)
			s.hooks.Emit(ctx, hooks.Event{
				Type:    hooks.EventUserMessage,
				Payload: map[string]any{"text": pendingMsg.Text, "channel_id": pendingMsg.ChannelID},
			})
			if s.extHooks != nil {
				// TODO: consider making fire-and-forget hooks async (goroutine) to avoid blocking the agent loop.
				s.extHooks.Run(ctx, "user_message", s.config.AgentName, map[string]any{
					"text":       pendingMsg.Text,
					"channel_id": pendingMsg.ChannelID,
				})
			}
			pendingMsg = nil
		}

		// Tool calls present — execute them and continue the loop.
		if len(resp.ToolCalls) > 0 {
			lastToolFailed = s.processToolCalls(ctx, resp, iter)
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
					llm.Message{Role: llm.RoleUser, Content: emptyResponseHint(lastToolFailed)},
				)
				emptyRetries = 0
			}
			continue
		}
		emptyRetries = 0

		// Phase 1: heuristic — obvious plan phrases or tool failure.
		if lastToolFailed || looksLikePlan(resp.Content) {
			lastToolFailed = false
			if nudges < maxNudges {
				s.ephemeralMessages = append(s.ephemeralMessages,
					llm.Message{Role: llm.RoleAssistant, Content: resp.Content},
					llm.Message{Role: llm.RoleUser, Content: nudgeMessage(resp.Content)},
				)
				nudges++
				s.logger.Debug("nudging LLM to use tools (heuristic)",
					zap.Int("nudge", nudges), zap.Int("iter", iter))
				continue
			}
			// Nudge budget exhausted — fall through to accept.
		}
		lastToolFailed = false

		// Phase 2: stuck escalation — if we already nudged and still no tool call,
		// inject a task-specific reminder instead of another generic nudge.
		// Gets one extra attempt beyond maxNudges since the task reminder is
		// qualitatively different from a generic nudge.
		if nudges >= 1 && nudges < maxNudges+1 {
			summary := s.lastTaskSummary
			if summary == "" {
				// No classifier summary available (e.g., Phase 1 heuristic fired
				// first). Fall back to the last user message as context.
				summary = s.lastUserMessage()
			}
			if summary != "" {
				s.ephemeralMessages = append(s.ephemeralMessages,
					llm.Message{Role: llm.RoleAssistant, Content: resp.Content},
					llm.Message{Role: llm.RoleUser, Content: taskReminderMessage(summary, resp.Content)},
				)
				nudges++
				s.logger.Debug("injecting task reminder (stuck escalation)",
					zap.Int("nudge", nudges), zap.Int("iter", iter),
					zap.String("task_summary", summary))
				continue
			}
		}

		// Phase 3: classifier for ambiguous short responses.
		if nudges == 0 && s.classifierLLM != nil && isAmbiguousResponse(resp.Content, s.tape) {
			lastUserMsg := s.lastUserMessage()
			toolNames := s.tools.Names()
			decision, taskSummary, ok := classifyResponse(
				ctx, s.classifierLLM, s.config.ClassifierModel,
				lastUserMsg, resp.Content, toolNames,
			)
			if ok {
				s.logger.Debug("classifier decision",
					zap.String("decision", decision),
					zap.String("task_summary", taskSummary))
				switch decision {
				case "nudge":
					s.ephemeralMessages = append(s.ephemeralMessages,
						llm.Message{Role: llm.RoleAssistant, Content: resp.Content},
						llm.Message{Role: llm.RoleUser, Content: nudgeMessage(resp.Content)},
					)
					nudges++
					continue
				case "stuck":
					s.lastTaskSummary = taskSummary
					s.ephemeralMessages = append(s.ephemeralMessages,
						llm.Message{Role: llm.RoleAssistant, Content: resp.Content},
						llm.Message{Role: llm.RoleUser, Content: taskReminderMessage(taskSummary, resp.Content)},
					)
					nudges++
					continue
				case "accept":
					// Fall through to accept.
				}
			}
			// Classifier failed to parse — fall through to accept.
		}

		// Final answer — no tool calls.
		content := s.processAssistantResponse(ctx, resp)
		return content, nil
	}

	return "Tool calling limit reached. Please try a simpler request.", nil
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
	pendingMsg *channel.IncomingMessage,
	skipHooks bool,
) (*llm.ChatResponse, error) {
	messages, err := s.buildMessages(ctx, maxTokens, iter, pendingMsg, skipHooks)
	if err != nil {
		return nil, err
	}

	req := s.buildLLMRequest(modelName, messages)

	s.hooks.Emit(ctx, hooks.Event{
		Type:    hooks.EventBeforeLLMCall,
		Payload: map[string]any{"iteration": iter, "message_count": len(messages)},
	})

	var resp *llm.ChatResponse
	if stream && tokenCh != nil {
		resp, err = s.doStreamingCall(ctx, req, tokenCh)
	} else {
		resp, err = s.llm.Chat(ctx, req)
	}
	if err != nil {
		// Invalidate chain on error; next call sends full context.
		s.chainValid = false
		s.hooks.Emit(ctx, hooks.Event{
			Type:    hooks.EventError,
			Payload: map[string]any{"error": err.Error()},
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
	pendingMsg *channel.IncomingMessage,
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
	pendingMsg *channel.IncomingMessage, iter int,
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

	injected, err := s.extHooks.Run(ctx, "before_llm_call", s.config.AgentName, map[string]any{
		"user_message":   userText,
		"iteration":      iter,
		"recent_context": recentContext,
		"message_count":  len(messages),
	})
	if err != nil {
		s.logger.Warn("external hook before_llm_call failed", zap.Error(err))
	} else if injected != "" {
		messages = append(messages, llm.Message{
			Role:    llm.RoleUser,
			Content: "[External context]\n" + injected,
		})
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

// processToolCalls records the assistant message, expands tool schemas, and
// executes each tool call in parallel, recording results to the tape in order.
// Returns true if any tool call failed.
func (s *Session) processToolCalls(
	ctx context.Context, resp *llm.ChatResponse, iter int,
) bool {
	s.appendMessage(llm.RoleAssistant, resp.Content, resp.ToolCalls, "", nil)

	// Auto-expand any tool the model calls, so the next iteration
	// sends the full parameter schema (progressive disclosure).
	for _, tc := range resp.ToolCalls {
		s.tools.ExpandAt(tc.Name, iter)
	}
	if resp.Content != "" {
		s.tools.ExpandHints(resp.Content)
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
			res := s.executeTool(gctx, tc)
			results.Store(i, toolResultEntry{id: tc.ID, name: tc.Name, result: res})
			return nil
		})
	}
	g.Wait()

	// Append results in original tool call order and track failures.
	// For skill results, expand any tools mentioned in the skill body so
	// the LLM has full parameter schemas when acting on the instructions.
	hadFailure := false
	for i := range resp.ToolCalls {
		v, _ := results.Load(i)
		r := v.(toolResultEntry)
		s.appendToolResult(r.id, r.name, r.result)

		if strings.HasPrefix(r.result, "Error:") {
			hadFailure = true
			s.toolFailures[r.name]++
			if s.toolFailures[r.name] >= maxToolFailures {
				s.appendMessage(llm.RoleUser,
					fmt.Sprintf("Tool %q has failed %d times this turn. Reconsider your approach — try a different tool or method.",
						r.name, s.toolFailures[r.name]), nil, "", nil)
			}
		}
	}
	return hadFailure
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

// executeTool runs a single tool call with hook emission.
func (s *Session) executeTool(
	ctx context.Context, tc llm.ToolCall,
) string {
	// Before tool exec hook — can block execution.
	if err := s.hooks.Emit(ctx, hooks.Event{
		Type: hooks.EventBeforeToolExec,
		Payload: map[string]any{
			"tool_name": tc.Name,
			"tool_id":   tc.ID,
			"arguments": tc.Arguments,
		},
	}); err != nil {
		return "Tool execution blocked: " + err.Error()
	}

	start := time.Now()
	result, err := s.tools.Execute(ctx, tc.Name, tc.Arguments)
	duration := time.Since(start)
	if err != nil {
		result = "Error: " + err.Error()
	}

	s.logger.Debug("tool executed",
		zap.String("tool", tc.Name),
		zap.Duration("duration", duration),
		zap.Bool("error", err != nil))

	s.hooks.Emit(ctx, hooks.Event{
		Type: hooks.EventAfterToolExec,
		Payload: map[string]any{
			"tool_name": tc.Name,
			"tool_id":   tc.ID,
			"result":    truncateForLog(result, maxLogTruncateLen),
		},
	})

	return result
}

// buildSystemPrompt constructs the system prompt for LLM calls.
// When persona files are configured, the prompt is assembled in three layers:
//
//	Layer 1 (Identity): SOUL.md, USER.md
//	Layer 2 (Operations): AGENTS.md + built-in tool-use instructions
//	Layer 3 (Knowledge): memory system description + MEMORY.md
//
// Falls back to the flat system-prompt.md approach when no persona exists.
func (s *Session) buildSystemPrompt() string {
	if s.config.Persona.HasPersona() {
		return s.buildPersonaPrompt()
	}
	return s.buildFlatPrompt()
}

// buildPersonaPrompt assembles the three-layer persona system prompt.
func (s *Session) buildPersonaPrompt() string {
	p := s.config.Persona
	var b strings.Builder

	soul := p.GetSoul()
	agents := p.GetAgents()
	memory := p.GetMemory()

	// --- Layer 1: Identity ---
	b.WriteString("# Identity\n\n")
	b.WriteString(soul)
	b.WriteByte('\n')
	if p.User != "" {
		b.WriteString("\n## User Profile\n\n")
		b.WriteString(p.User)
		b.WriteByte('\n')
	}

	// --- Layer 2: Operations ---
	b.WriteString("\n# Operations\n\n")
	if agents != "" {
		b.WriteString(agents)
		b.WriteByte('\n')
	}
	b.WriteString("\n## Tool Use\n\n")
	b.WriteString(toolUseInstruction)

	// --- Layer 3: Knowledge ---
	b.WriteString("\n# Knowledge\n\n")
	b.WriteString("Use the persona_self tool to read/update your persona files: ")
	b.WriteString("SOUL.md (identity), AGENTS.md (rules), MEMORY.md (knowledge & preferences). ")
	b.WriteString("Update MEMORY.md regularly for learned patterns and user preferences.\n")

	if memory != "" {
		b.WriteString("\n## Current Memory\n\n")
		b.WriteString(memory)
		b.WriteByte('\n')
	}

	// --- Environment ---
	b.WriteString("\n# Environment\n\n")
	fmt.Fprintf(&b, "Working directory: %s\n", s.config.WorkspaceDir)
	fmt.Fprintf(&b, "Current time: %s\n", time.Now().Format(time.RFC3339))

	return b.String()
}

// buildFlatPrompt is the legacy system prompt assembly (no persona files).
func (s *Session) buildFlatPrompt() string {
	var b strings.Builder

	b.WriteString("You are golem, a helpful coding assistant.\n\n")

	fmt.Fprintf(&b, "Working directory: %s\n", s.config.WorkspaceDir)
	fmt.Fprintf(&b, "Current time: %s\n\n", time.Now().Format(time.RFC3339))

	b.WriteString(toolUseInstruction)
	b.WriteByte('\n')

	switch {
	case s.config.SystemPrompt != "":
		b.WriteString(s.config.SystemPrompt)
		b.WriteByte('\n')
	default:
		if data, err := os.ReadFile(".agent/system-prompt.md"); err == nil {
			b.WriteString(strings.TrimSpace(string(data)))
			b.WriteByte('\n')
		}
	}

	return b.String()
}

// appendMessage records a message to the tape.
// senderID is optional and only set for user messages in group chats.
// images stores metadata-only refs (media_type) to avoid bloating the tape.
func (s *Session) appendMessage(
	role llm.Role, content string,
	toolCalls []llm.ToolCall, senderID string,
	images []llm.ImageContent,
) {
	payload := map[string]any{
		"role":    string(role),
		"content": content,
	}
	if len(toolCalls) > 0 {
		payload["tool_calls"] = toolCalls
	}
	if senderID != "" {
		payload["sender_id"] = senderID
	}
	if len(images) > 0 {
		// Store metadata only — no base64 data in the tape.
		refs := make([]map[string]string, len(images))
		for i, img := range images {
			refs[i] = map[string]string{"media_type": img.MediaType}
		}
		payload["images"] = refs
	}

	s.tape.Append(tape.TapeEntry{
		Kind:    tape.KindMessage,
		Payload: tape.MarshalPayload(payload),
	})

	// Track for Responses API incremental input.
	if s.config.UseResponsesAPI {
		msg := llm.Message{
			Role:    role,
			Content: content,
		}
		if len(toolCalls) > 0 {
			msg.ToolCalls = toolCalls
		}
		if len(images) > 0 {
			msg.Images = images
		}
		s.incrementalMessages = append(s.incrementalMessages, msg)
	}
}

// appendToolResult records a tool result to the tape with proper metadata.
func (s *Session) appendToolResult(
	toolCallID, toolName, result string,
) {
	s.tape.Append(tape.TapeEntry{
		Kind: tape.KindMessage,
		Payload: tape.MarshalPayload(map[string]any{
			"role":         string(llm.RoleTool),
			"content":      result,
			"tool_call_id": toolCallID,
			"name":         toolName,
		}),
	})

	// Track for Responses API incremental input.
	if s.config.UseResponsesAPI {
		s.incrementalMessages = append(s.incrementalMessages, llm.Message{
			Role:       llm.RoleTool,
			Content:    result,
			ToolCallID: toolCallID,
			Name:       toolName,
		})
	}
}

// truncateForLog truncates a string to maxLen and appends "..." if truncated.
func truncateForLog(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}

// RecordFeedback appends a KindFeedback entry to the tape.
func (s *Session) RecordFeedback(chatID, value string) {
	s.tape.Append(tape.TapeEntry{
		Kind: tape.KindFeedback,
		Payload: tape.MarshalPayload(map[string]string{
			"chat_id": chatID,
			"value":   value,
		}),
	})
}

// StatusInfo returns a human-readable status summary for this session.
func (s *Session) StatusInfo() string {
	model := s.config.Model
	totalTokens := s.sessionUsage.TotalTokens
	promptTokens := s.sessionUsage.PromptTokens
	completionTokens := s.sessionUsage.CompletionTokens
	toolCount := s.tools.Count()

	return fmt.Sprintf(
		"**Model:** %s\n**Tools:** %d\n**Tokens used:** %d (prompt: %d, completion: %d)",
		model, toolCount, totalTokens, promptTokens, completionTokens,
	)
}

// Summarize generates a summary of the current conversation and appends it
// to the tape as a KindSummary entry. This is called before tape rotation or
// session teardown so that restored sessions carry forward context.
// Returns the summary text and any error.
func (s *Session) Summarize(ctx context.Context) (string, error) {
	s.logger.Debug("summarization starting")
	entries, err := s.tape.Entries()
	if err != nil {
		return "", fmt.Errorf("summarize: reading tape: %w", err)
	}
	msgs := tape.BuildMessages(entries)
	if len(msgs) < 2 {
		return "", nil // not enough conversation to summarize
	}

	// Limit to the last N messages to keep the summarization call small.
	if len(msgs) > maxSummaryMessages {
		msgs = msgs[len(msgs)-maxSummaryMessages:]
	}

	summaryPrompt := "Summarize this conversation using the following structured format. " +
		"Use the same language the user was speaking.\n\n" +
		"TOPIC: <one-line description of the main subject>\n" +
		"DECISIONS:\n- <bullet list of decisions made>\n" +
		"OUTCOMES:\n- <bullet list of what was accomplished>\n" +
		"PENDING:\n- <bullet list of unfinished items, if any>\n" +
		"KEY FACTS:\n- <bullet list of important names, IDs, values, or context for future reference>"

	summaryMsgs := append(msgs, llm.Message{
		Role:    llm.RoleUser,
		Content: summaryPrompt,
	})

	_, modelName := llm.ParseModelProvider(s.config.Model)
	resp, err := s.llm.Chat(ctx, llm.ChatRequest{
		Model:     modelName,
		Messages:  summaryMsgs,
		MaxTokens: 2048,
	})
	if err != nil {
		return "", fmt.Errorf("summarize: LLM call: %w", err)
	}

	s.logger.Debug("summarization complete",
		zap.Int("summary_len", len(resp.Content)))

	err = s.tape.Append(tape.TapeEntry{
		Kind: tape.KindSummary,
		Payload: tape.MarshalPayload(map[string]string{
			"summary": resp.Content,
		}),
	})
	return resp.Content, err
}
