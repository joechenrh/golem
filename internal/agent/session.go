package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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

	// Lifecycle fields (managed by SessionManager for remote chats;
	// unused for the default CLI session).
	ctx        context.Context
	cancel     context.CancelFunc
	lastAccess time.Time
	TapePath   string
}

const maxToolFailures = 3

// NewSession creates a Session with all dependencies wired in.
func NewSession(
	llmClient llm.Client,
	toolRegistry *tools.Registry,
	tapeStore tape.Store,
	ctxStrategy ctxmgr.ContextStrategy,
	hookBus *hooks.Bus,
	cfg *config.Config,
	logger *zap.Logger,
) *Session {
	return &Session{
		llm:             llmClient,
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
	_, modelName := llm.ParseModelProvider(s.config.Model)
	maxTokens := ctxmgr.ModelContextWindow(modelName)

	// Reset per-turn tracking.
	s.turnUsage = llm.Usage{}
	s.toolFailures = make(map[string]int)

	const maxNudges = 2
	nudges := 0

	for iter := range s.config.MaxToolIter {
		resp, err := s.executeLLMCall(ctx, modelName, maxTokens, iter, stream, tokenCh, pendingMsg)
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
			pendingMsg = nil
		}

		// Tool calls present — execute them and continue the loop.
		if len(resp.ToolCalls) > 0 {
			s.processToolCalls(ctx, resp)
			continue
		}

		// Empty response with no tool calls — retry instead of
		// returning a blank answer to the user.
		if strings.TrimSpace(resp.Content) == "" {
			s.logger.Warn("LLM returned empty response, retrying",
				zap.Int("iter", iter))
			continue
		}

		// No tool calls. If the response looks like a plan rather than a
		// final answer, nudge the LLM to actually use tools.
		if nudges < maxNudges && looksLikePlan(resp.Content) {
			s.appendMessage(llm.RoleAssistant, resp.Content, nil, "", nil)
			s.appendMessage(llm.RoleUser, nudgeMessage(resp.Content), nil, "", nil)
			nudges++
			s.logger.Debug("nudging LLM to use tools",
				zap.Int("nudge", nudges), zap.Int("iter", iter))
			continue
		}

		// Final answer — no tool calls.
		content := s.processAssistantResponse(ctx, resp)
		return content, nil
	}

	return "Tool calling limit reached. Please try a simpler request.", nil
}

// planCheckPrefixLen is the number of characters at the start of a response
// to check for intent phrases. Plans open with intent; greetings or
// answers that happen to contain intent words deeper in the text should
// not trigger a nudge.
const planCheckPrefixLen = 200

// looksLikePlan returns true if the opening of the content appears to
// describe intended actions rather than providing a final answer.
func looksLikePlan(content string) bool {
	prefix := content
	if len(prefix) > planCheckPrefixLen {
		prefix = prefix[:planCheckPrefixLen]
	}

	lower := strings.ToLower(prefix)
	for _, phrase := range []string{
		"i'll ", "i will ", "let me ", "i'm going to ",
		"i'll\n", "i will\n", "let me\n",
		"first, i'll", "first, let me",
		"i can help", "i can do",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	// Chinese intent phrases must appear at a sentence boundary:
	// start of text, or after a newline / period / comma / exclamation.
	for _, phrase := range []string{
		"我来", "让我", "我会", "我将",
		"首先", "接下来我",
	} {
		if startsWithPhrase(prefix, phrase) {
			return true
		}
	}
	return false
}

// startsWithPhrase checks if phrase appears at the start of text or
// immediately after a sentence boundary (newline or CJK punctuation).
func startsWithPhrase(text, phrase string) bool {
	idx := strings.Index(text, phrase)
	if idx < 0 {
		return false
	}
	if idx == 0 {
		return true
	}
	// Check the rune immediately before the match.
	for i := idx - 1; i >= 0; i-- {
		r := rune(text[i])
		// Skip whitespace.
		if r == ' ' || r == '\t' {
			continue
		}
		// Sentence boundaries.
		switch r {
		case '\n', '.', ',', '!', '?',
			'\u3002', // fullwidth period
			'\uff0c', // fullwidth comma
			'\uff01', // fullwidth exclamation
			'\uff1f': // fullwidth question mark
			return true
		}
		// Part of a larger word/phrase — not a boundary.
		return false
	}
	return true
}

// nudgeMessage returns a nudge prompt in the same language as the content.
func nudgeMessage(content string) string {
	if isMostlyCJK(content) {
		return "不要只描述你打算做什么——现在就使用可用的工具来执行。"
	}
	return "Don't just describe what you'll do — use the available tools now to proceed."
}

// isMostlyCJK returns true if CJK characters make up the majority of
// non-whitespace, non-punctuation runes in the text.
func isMostlyCJK(s string) bool {
	var cjk, other int
	for _, r := range s {
		if r <= ' ' {
			continue
		}
		if r >= 0x2E80 && r <= 0x9FFF || r >= 0xF900 && r <= 0xFAFF {
			cjk++
		} else {
			other++
		}
	}
	return cjk > other
}

// executeLLMCall builds context, calls the LLM (streaming or not), and emits hooks.
// If pendingMsg is non-nil, its text is appended to the context as a user
// message without persisting to the tape, so that a failed API call
// does not leave a dangling tape entry.
func (s *Session) executeLLMCall(
	ctx context.Context, modelName string,
	maxTokens, iter int, stream bool,
	tokenCh chan<- string,
	pendingMsg *channel.IncomingMessage,
) (*llm.ChatResponse, error) {
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

	systemPrompt := s.buildSystemPrompt()
	toolDefs := s.tools.ToolDefinitions()

	s.hooks.Emit(ctx, hooks.Event{
		Type:    hooks.EventBeforeLLMCall,
		Payload: map[string]any{"iteration": iter, "message_count": len(messages)},
	})

	req := llm.ChatRequest{
		Model:        modelName,
		SystemPrompt: systemPrompt,
		Messages:     messages,
		Tools:        toolDefs,
		MaxTokens:    s.config.MaxOutputTokens,
		Temperature:  s.config.Temperature,
	}

	var resp *llm.ChatResponse
	if stream && tokenCh != nil {
		resp, err = s.doStreamingCall(ctx, req, tokenCh)
	} else {
		resp, err = s.llm.Chat(ctx, req)
	}
	if err != nil {
		s.hooks.Emit(ctx, hooks.Event{
			Type:    hooks.EventError,
			Payload: map[string]any{"error": err.Error()},
		})
		return nil, fmt.Errorf("LLM call: %w", err)
	}

	// Accumulate token usage.
	s.turnUsage.PromptTokens += resp.Usage.PromptTokens
	s.turnUsage.CompletionTokens += resp.Usage.CompletionTokens
	s.turnUsage.TotalTokens += resp.Usage.TotalTokens
	s.sessionUsage.PromptTokens += resp.Usage.PromptTokens
	s.sessionUsage.CompletionTokens += resp.Usage.CompletionTokens
	s.sessionUsage.TotalTokens += resp.Usage.TotalTokens

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

	return resp, nil
}

// processToolCalls records the assistant message, expands tool schemas, and
// executes each tool call in parallel, recording results to the tape in order.
func (s *Session) processToolCalls(
	ctx context.Context, resp *llm.ChatResponse,
) {
	s.appendMessage(llm.RoleAssistant, resp.Content, resp.ToolCalls, "", nil)

	// Auto-expand any tool the model calls, so the next iteration
	// sends the full parameter schema (progressive disclosure).
	for _, tc := range resp.ToolCalls {
		s.tools.Expand(tc.Name)
	}
	if resp.Content != "" {
		s.tools.ExpandHints(resp.Content)
	}

	// Execute tool calls in parallel and collect results in order.
	type toolResultEntry struct {
		id     string
		name   string
		result string
	}
	results := make([]toolResultEntry, len(resp.ToolCalls))
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	for i, tc := range resp.ToolCalls {
		g.Go(func() error {
			res := s.executeTool(gctx, tc)
			mu.Lock()
			results[i] = toolResultEntry{id: tc.ID, name: tc.Name, result: res}
			mu.Unlock()
			return nil
		})
	}
	g.Wait()

	// Append results in the original order so the tape is deterministic.
	// Track per-tool failure counts for self-correction.
	for _, r := range results {
		s.appendToolResult(r.id, r.name, r.result)

		if strings.HasPrefix(r.result, "Error:") {
			s.toolFailures[r.name]++
			if s.toolFailures[r.name] >= maxToolFailures {
				s.appendMessage(llm.RoleUser,
					fmt.Sprintf("Tool %q has failed %d times this turn. Reconsider your approach — try a different tool or method.",
						r.name, s.toolFailures[r.name]), nil, "", nil)
			}
		}
	}
}

// processAssistantResponse handles the final answer: runs any embedded comma
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

	result, err := s.tools.Execute(ctx, tc.Name, tc.Arguments)
	if err != nil {
		result = "Error: " + err.Error()
	}

	s.hooks.Emit(ctx, hooks.Event{
		Type: hooks.EventAfterToolExec,
		Payload: map[string]any{
			"tool_name": tc.Name,
			"tool_id":   tc.ID,
			"result":    truncateForLog(result, 500),
		},
	})

	return result
}

// handleCommand dispatches an internal or shell colon-command.
func (s *Session) handleCommand(
	ctx context.Context, route router.RouteResult,
) (string, error) {
	switch route.Kind {
	case router.CommandInternal:
		return s.handleInternalCommand(ctx, route.Command, route.Args)
	case router.CommandShell:
		// Shell commands are executed via the shell_exec tool.
		args, _ := json.Marshal(map[string]string{"command": route.Command})
		result, err := s.tools.Execute(ctx, "shell_exec", string(args))
		if err != nil {
			return "Error: " + err.Error(), nil
		}
		return result, nil
	}
	return "", nil
}

// handleInternalCommand processes built-in colon-commands.
func (s *Session) handleInternalCommand(
	_ context.Context, cmd, args string,
) (string, error) {
	switch cmd {
	case "help":
		return s.helpText(), nil

	case "quit":
		return "", ErrQuit

	case "tape.info":
		info := s.tape.Info()
		return fmt.Sprintf("Tape: %s\nEntries: %d | Anchors: %d | Since last anchor: %d",
			info.FilePath, info.TotalEntries, info.AnchorCount, info.EntriesSinceAnchor), nil

	case "tape.search":
		if args == "" {
			return "Usage: :tape.search <query>", nil
		}
		results, err := s.tape.Search(args)
		if err != nil {
			return "Error: " + err.Error(), nil
		}
		if len(results) == 0 {
			return "No matches found.", nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Found %d matches:\n", len(results))
		for _, e := range results {
			fmt.Fprintf(&b, "  [%s] %s: %s\n", e.Kind, e.Timestamp.Format(time.RFC3339), truncateForLog(string(e.Payload), 100))
		}
		return b.String(), nil

	case "tools":
		return s.tools.List(), nil

	case "skills":
		list := s.tools.List()
		// Extract just the skills section.
		if idx := strings.Index(list, "Skills"); idx >= 0 {
			return list[idx:], nil
		}
		return "No skills registered.", nil

	case "model":
		if args == "" {
			return fmt.Sprintf("Current model: %s (provider: %s)", s.config.Model, s.llm.Provider()), nil
		}
		// Model switching would require creating a new client — for now just report.
		return fmt.Sprintf("Model switching is not yet supported. Current: %s", s.config.Model), nil

	case "usage":
		return fmt.Sprintf("Session tokens: prompt=%d completion=%d total=%d\nLast turn:      prompt=%d completion=%d total=%d",
			s.sessionUsage.PromptTokens, s.sessionUsage.CompletionTokens, s.sessionUsage.TotalTokens,
			s.turnUsage.PromptTokens, s.turnUsage.CompletionTokens, s.turnUsage.TotalTokens), nil

	case "metrics":
		if s.MetricsSummary != nil {
			return s.MetricsSummary(), nil
		}
		return "Metrics not available.", nil

	case "reset":
		label := args
		if label == "" {
			label = "manual"
		}
		if err := s.tape.AddAnchor(label); err != nil {
			return "Error: " + err.Error(), nil
		}
		return fmt.Sprintf("Anchor added: %s", label), nil

	default:
		return fmt.Sprintf("Unknown command: %s. Type :help for available commands.", cmd), nil
	}
}

func (s *Session) helpText() string {
	return `Available commands:
  :help              Show this help message
  :quit              Exit golem
  :usage             Show token usage statistics
  :metrics           Show operational metrics
  :tape.info         Show tape statistics
  :tape.search <q>   Search tape history
  :tools             List registered tools
  :skills            List discovered skills
  :model [name]      Show or change current model
  :reset [label]     Add a tape anchor (context boundary)
  :<command>         Execute a shell command (e.g., :ls -la)`
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
	b.WriteString("When you need to perform actions, use the available tools immediately. ")
	b.WriteString("You may briefly explain your reasoning alongside tool calls, but always ")
	b.WriteString("include the tool calls in the same response — never respond with only a ")
	b.WriteString("plan or description of what you intend to do.\n")

	// --- Layer 3: Knowledge ---
	b.WriteString("\n# Knowledge\n\n")
	b.WriteString("## Persona Files\n\n")
	b.WriteString("You can read and modify your own persona files using the persona_self tool:\n\n")
	b.WriteString("- **SOUL.md** (≤100 lines): Your core identity and personality. ")
	b.WriteString("Modify only when the user explicitly requests an identity change (very rare).\n")
	b.WriteString("- **AGENTS.md** (≤150 lines): Your behavioral rules. ")
	b.WriteString("Update when behavioral rules need evolving based on experience (somewhat rare).\n")
	b.WriteString("- **MEMORY.md** (≤200 lines): Your curated, distilled knowledge. ")
	b.WriteString("Update regularly for learned preferences, hard-won lessons, stable patterns.\n\n")
	b.WriteString("SOUL.md and AGENTS.md are backed up to .bak before overwriting.\n\n")
	b.WriteString("- **mnemos** (shared): Cross-agent long-term memory store. Use it for facts, ")
	b.WriteString("research results, contextual details, and raw material that any agent can retrieve. ")
	b.WriteString("Use memory_store / memory_recall tools.\n\n")
	b.WriteString("Principle: mnemos is the warehouse; MEMORY.md is the distilled memo.\n")

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

	b.WriteString("When you need to perform actions, use the available tools immediately. ")
	b.WriteString("You may briefly explain your reasoning alongside tool calls, but always ")
	b.WriteString("include the tool calls in the same response — never respond with only a ")
	b.WriteString("plan or description of what you intend to do.\n\n")

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
}

// ErrQuit signals that the user wants to quit.
var ErrQuit = errors.New("quit")

func truncateForLog(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}

// Summarize generates a summary of the current conversation and appends it
// to the tape as a KindSummary entry. This is called before tape rotation or
// session teardown so that restored sessions carry forward context.
func (s *Session) Summarize(ctx context.Context) error {
	entries, err := s.tape.Entries()
	if err != nil {
		return fmt.Errorf("summarize: reading tape: %w", err)
	}
	msgs := tape.BuildMessages(entries)
	if len(msgs) < 2 {
		return nil // not enough conversation to summarize
	}

	// Limit to the last 50 messages to keep the summarization call small.
	if len(msgs) > 50 {
		msgs = msgs[len(msgs)-50:]
	}

	summaryPrompt := "Summarize the key points, decisions, and outcomes from this conversation in 3-5 concise bullet points. " +
		"Focus on what was done, what was decided, and any important context for future reference. " +
		"Use the same language the user was speaking."

	summaryMsgs := append(msgs, llm.Message{
		Role:    llm.RoleUser,
		Content: summaryPrompt,
	})

	_, modelName := llm.ParseModelProvider(s.config.Model)
	resp, err := s.llm.Chat(ctx, llm.ChatRequest{
		Model:     modelName,
		Messages:  summaryMsgs,
		MaxTokens: 1024,
	})
	if err != nil {
		return fmt.Errorf("summarize: LLM call: %w", err)
	}

	return s.tape.Append(tape.TapeEntry{
		Kind: tape.KindSummary,
		Payload: tape.MarshalPayload(map[string]string{
			"summary": resp.Content,
		}),
	})
}
