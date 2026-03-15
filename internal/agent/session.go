// Package agent implements the ReAct loop for autonomous tool-using AI assistants.
// session.go contains the Session type definition, constructor, public API
// (HandleInput/HandleInputStream), tape operations, and lifecycle methods.
// The ReAct loop logic is in react.go and LLM call assembly is in llm_call.go.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/joechenrh/golem/internal/channel"
	"github.com/joechenrh/golem/internal/config"
	"github.com/joechenrh/golem/internal/ctxmgr"
	"github.com/joechenrh/golem/internal/hooks"
	"github.com/joechenrh/golem/internal/llm"
	"github.com/joechenrh/golem/internal/redact"
	"github.com/joechenrh/golem/internal/router"
	"github.com/joechenrh/golem/internal/tape"
	"github.com/joechenrh/golem/internal/tools"
)

// IncomingMessage is an alias for channel.IncomingMessage used within this package.
type IncomingMessage = channel.IncomingMessage

// Session orchestrates the ReAct loop for a single conversation: LLM calls,
// tool execution, tape recording, command routing, and token tracking.
// Each conversation (CLI or remote chat) gets its own Session.
//
// For remote channels (e.g. Lark), multiple messages may arrive for the
// same chat concurrently. The mu mutex serializes access so that only one
// HandleInput/HandleInputStream call runs at a time per Session.
type Session struct {
	mu sync.Mutex // serializes HandleInput/HandleInputStream calls

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

	// Redactor strips secrets from tool arguments before tape persistence.
	redactor *redact.Redactor

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

	// Background task tracker for async subagent orchestration.
	tasks *TaskTracker

	// Session identity for log context and hook payloads.
	sessionID string

	// Collaborators: extracted to reduce Session complexity.
	toolExec    *ToolExecutor
	classifier  *NudgeClassifier
	prompt      *PromptBuilder
	accumulator *EventAccumulator

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

	// extra iterations granted after MaxToolIter when background tasks are pending
	taskRecoveryIters = 3

	// max chars for log truncation
	maxLogTruncateLen = 500

	// max messages to include in summarization
	maxSummaryMessages = 80
)

// toolUseInstruction is the shared tool-use guidance included in all system prompts.
const toolUseInstruction = "When you have enough information to act, call tools directly — " +
	"don't describe what you plan to do. If the user's request is ambiguous or " +
	"missing key details, ask a brief clarifying question first. " +
	"Keep narration brief; default to action over explanation.\n" +
	"For code-related tasks (writing code, debugging, file analysis, multi-step investigations), " +
	"always use spawn_agent to delegate the work to background sub-agents instead of doing it " +
	"directly in the main session. This keeps you responsive to the user while sub-agents work. " +
	"Spawn multiple sub-agents in one response for parallel execution. " +
	"You will receive sub-agent results automatically when they finish — no need to poll. " +
	"After receiving results, decide whether to spawn follow-up agents, retry failures, " +
	"or deliver the final answer.\n"

// ExtHookRunner is satisfied by exthook.Runner.
// Defined here as an interface to avoid a circular import.
type ExtHookRunner interface {
	Run(ctx context.Context, event string, agentName string, data map[string]any) (string, error)
}

// NewSession creates a Session with all dependencies wired in.
// sessionID is used as a log context field and included in hook payloads.
func NewSession(
	llmClient llm.Client,
	classifierLLM llm.Client,
	toolRegistry *tools.Registry,
	tapeStore tape.Store,
	ctxStrategy ctxmgr.ContextStrategy,
	hookBus *hooks.Bus,
	cfg *config.Config,
	logger *zap.Logger,
	sessionID string,
) *Session {
	namedLogger := logger.With(zap.String("session", sessionID))
	tasks := NewTaskTracker(5)
	redactor := redact.New()

	s := &Session{
		llm:             llmClient,
		classifierLLM:   classifierLLM,
		tools:           toolRegistry,
		tape:            tapeStore,
		contextStrategy: ctxStrategy,
		hooks:           hookBus,
		config:          cfg,
		logger:          namedLogger,
		sessionID:       sessionID,
		tasks:           tasks,
		redactor:        redactor,
	}

	s.toolExec = &ToolExecutor{
		tools:     toolRegistry,
		hooks:     hookBus,
		tasks:     tasks,
		redactor:  redactor,
		logger:    namedLogger,
		sessionID: sessionID,
	}

	s.classifier = &NudgeClassifier{
		classifierLLM:   classifierLLM,
		classifierModel: cfg.ClassifierModel,
		logger:          namedLogger,
	}

	s.prompt = &PromptBuilder{
		config: cfg,
		tools:  toolRegistry,
		logger: namedLogger,
	}

	return s
}

// SetSkillReload configures periodic skill reload from the given directories.
func (s *Session) SetSkillReload(dirs []string, interval time.Duration) {
	s.skillDirs = dirs
	s.skillReloadInterval = interval
	s.prompt.skillDirs = dirs
	s.prompt.skillReloadInterval = interval
}

// SetExtHooks sets the external hook runner for this session.
func (s *Session) SetExtHooks(runner ExtHookRunner) {
	s.extHooks = runner
}

// SetAccumulator attaches an EventAccumulator for progress tracking.
func (s *Session) SetAccumulator(acc *EventAccumulator) {
	s.accumulator = acc
}

// Accumulator returns the session's EventAccumulator, or nil if none is set.
func (s *Session) Accumulator() *EventAccumulator {
	return s.accumulator
}

// Tasks returns the session's TaskTracker.
func (s *Session) Tasks() *TaskTracker {
	return s.tasks
}

// Close cancels all background tasks and waits for goroutines to finish.
// Must be called before cancelling the session context.
func (s *Session) Close() {
	if s.tasks != nil {
		s.tasks.Close()
	}
}

// HandleInput processes a user message and returns the final response.
// Used by non-streaming channels. Serialized per session via mu.
func (s *Session) HandleInput(
	ctx context.Context, msg channel.IncomingMessage,
) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx = channel.WithChannelID(ctx, msg.ChannelID)

	route := router.RouteUser(msg.Text)
	if route.IsCommand {
		return s.handleCommand(ctx, route)
	}

	return s.runReActLoop(ctx, false, nil, &msg)
}

// HandleInputStream processes a user message with streaming.
// Tokens are sent to tokenCh as they arrive. Used by CLI.
// Serialized per session via mu.
func (s *Session) HandleInputStream(
	ctx context.Context, msg channel.IncomingMessage,
	tokenCh chan<- string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx = channel.WithChannelID(ctx, msg.ChannelID)

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

// --- Tape operations ---

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
		// Redact secrets from tool arguments before tape persistence.
		redacted := make([]llm.ToolCall, len(toolCalls))
		copy(redacted, toolCalls)
		for i := range redacted {
			redacted[i].Arguments = s.redactor.Redact(redacted[i].Arguments)
		}
		payload["tool_calls"] = redacted
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

// wireAutoAnchor sets up the OnTrim callback on the context strategy so that
// when trimToFit drops messages, an anchor is automatically inserted in the
// tape. This makes context trimming self-maintaining — users don't need to
// manually run :reset to prevent unbounded tape growth.
func (s *Session) wireAutoAnchor() {
	onTrim := func(droppedCount int) {
		s.tape.Append(tape.TapeEntry{
			Kind: tape.KindAnchor,
			Payload: tape.MarshalPayload(map[string]string{
				"label": fmt.Sprintf("auto-trim: %d messages dropped", droppedCount),
			}),
		})
		s.logger.Info("auto-anchor inserted after context trimming",
			zap.Int("dropped", droppedCount))
	}

	switch cs := s.contextStrategy.(type) {
	case *ctxmgr.AnchorStrategy:
		cs.OnTrim = onTrim
	case *ctxmgr.MaskingStrategy:
		cs.OnTrim = onTrim
	case *ctxmgr.HybridStrategy:
		// HybridStrategy already has OnDrop; also wire OnTrim
		// via its trimWithCallback which fires OnDrop directly.
	}
}

// persistExpandedTools saves the current progressive disclosure state to the tape
// so that restored sessions don't re-expand tools from scratch.
func (s *Session) persistExpandedTools() {
	names := s.tools.ExpandedNames()
	if len(names) == 0 {
		return
	}
	s.tape.Append(tape.TapeEntry{
		Kind: tape.KindEvent,
		Payload: tape.MarshalPayload(map[string]any{
			"event":          "expanded_tools",
			"expanded_tools": names,
		}),
	})
}

// RestoreExpandedTools reads the latest expanded_tools event from tape entries
// and restores the progressive disclosure state in the tool registry.
func (s *Session) RestoreExpandedTools() {
	entries, err := s.tape.Entries()
	if err != nil {
		return
	}
	// Walk backwards to find the most recent expanded_tools event.
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind != tape.KindEvent {
			continue
		}
		var payload struct {
			Event         string   `json:"event"`
			ExpandedTools []string `json:"expanded_tools"`
		}
		if json.Unmarshal(entries[i].Payload, &payload) == nil && payload.Event == "expanded_tools" {
			s.tools.RestoreExpanded(payload.ExpandedTools)
			s.logger.Debug("restored expanded tools from tape",
				zap.Int("count", len(payload.ExpandedTools)))
			return
		}
	}
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

// StatusInfo returns a human-readable summary of the session.
// NOTE: This method intentionally does NOT acquire s.mu so it can be called
// concurrently while a ReAct loop is running (e.g., from /status commands).
// Usage counters may be slightly stale, which is acceptable for display.
// When the session is actively running and has an accumulator, progress
// information is appended to the base model/token info.
func (s *Session) StatusInfo() string {
	model := s.config.Model
	totalTokens := s.sessionUsage.TotalTokens
	promptTokens := s.sessionUsage.PromptTokens
	completionTokens := s.sessionUsage.CompletionTokens
	toolCount := s.tools.Count()

	base := fmt.Sprintf(
		"**Model:** %s\n**Tools:** %d\n**Tokens used:** %d (prompt: %d, completion: %d)",
		model, toolCount, totalTokens, promptTokens, completionTokens,
	)

	if s.accumulator != nil {
		snap := s.accumulator.Snapshot()
		if snap.State.IdleSince == nil && snap.State.Iteration > 0 {
			return base + "\n\n" + FormatProgress(snap)
		}
	}

	return base
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
