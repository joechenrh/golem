package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/joechenrh/golem/internal/channel"
	"github.com/joechenrh/golem/internal/config"
	"github.com/joechenrh/golem/internal/ctxmgr"
	"github.com/joechenrh/golem/internal/hooks"
	"github.com/joechenrh/golem/internal/llm"
	"github.com/joechenrh/golem/internal/router"
	"github.com/joechenrh/golem/internal/tape"
	"github.com/joechenrh/golem/internal/tools"
)

// AgentLoop orchestrates the ReAct loop: LLM calls, tool execution, routing.
type AgentLoop struct {
	llm             llm.Client
	tools           *tools.Registry
	tape            tape.Store
	contextStrategy ctxmgr.ContextStrategy
	hooks           *hooks.Bus
	config          *config.Config
	logger          *zap.Logger
}

// New creates an AgentLoop with all dependencies wired in.
func New(
	llmClient llm.Client,
	toolRegistry *tools.Registry,
	tapeStore tape.Store,
	ctxStrategy ctxmgr.ContextStrategy,
	hookBus *hooks.Bus,
	cfg *config.Config,
	logger *zap.Logger,
) *AgentLoop {
	return &AgentLoop{
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
func (a *AgentLoop) HandleInput(ctx context.Context, msg channel.IncomingMessage) (string, error) {
	// Route user input.
	route := router.RouteUser(msg.Text)
	if route.IsCommand {
		return a.handleCommand(ctx, route)
	}

	// Record user message to tape.
	a.appendMessage(llm.RoleUser, msg.Text, nil, msg.SenderID)

	// Emit user message hook.
	a.hooks.Emit(ctx, hooks.Event{
		Type:    hooks.EventUserMessage,
		Payload: map[string]any{"text": msg.Text, "channel_id": msg.ChannelID},
	})

	return a.runReActLoop(ctx, false, nil)
}

// HandleInputStream processes a user message with streaming.
// Tokens are sent to tokenCh as they arrive. Used by CLI.
func (a *AgentLoop) HandleInputStream(ctx context.Context, msg channel.IncomingMessage, tokenCh chan<- string) error {
	// Route user input.
	route := router.RouteUser(msg.Text)
	if route.IsCommand {
		result, err := a.handleCommand(ctx, route)
		if err != nil {
			return err
		}
		tokenCh <- result
		return nil
	}

	// Record user message to tape.
	a.appendMessage(llm.RoleUser, msg.Text, nil, msg.SenderID)

	// Emit user message hook.
	a.hooks.Emit(ctx, hooks.Event{
		Type:    hooks.EventUserMessage,
		Payload: map[string]any{"text": msg.Text, "channel_id": msg.ChannelID},
	})

	_, err := a.runReActLoop(ctx, true, tokenCh)
	return err
}

// runReActLoop executes the tool-calling loop until the LLM produces a final answer
// or the iteration limit is reached.
func (a *AgentLoop) runReActLoop(ctx context.Context, stream bool, tokenCh chan<- string) (string, error) {
	_, modelName := llm.ParseModelProvider(a.config.Model)
	maxTokens := ctxmgr.ModelContextWindow(modelName)

	for iter := range a.config.MaxToolIter {
		resp, err := a.executeLLMCall(ctx, modelName, maxTokens, iter, stream, tokenCh)
		if err != nil {
			return "", err
		}

		// Tool calls present — execute them and continue the loop.
		if len(resp.ToolCalls) > 0 {
			a.processToolCalls(ctx, resp)
			continue
		}

		// Final answer — no tool calls.
		content := a.processAssistantResponse(ctx, resp)
		return content, nil
	}

	return "Tool calling limit reached. Please try a simpler request.", nil
}

// executeLLMCall builds context, calls the LLM (streaming or not), and emits hooks.
func (a *AgentLoop) executeLLMCall(ctx context.Context, modelName string, maxTokens, iter int, stream bool, tokenCh chan<- string) (*llm.ChatResponse, error) {
	entries, err := a.tape.Entries()
	if err != nil {
		return nil, fmt.Errorf("reading tape: %w", err)
	}
	messages, err := a.contextStrategy.BuildContext(ctx, entries, maxTokens)
	if err != nil {
		return nil, fmt.Errorf("building context: %w", err)
	}

	systemPrompt := a.buildSystemPrompt()
	toolDefs := a.tools.ToolDefinitions()

	a.hooks.Emit(ctx, hooks.Event{
		Type:    hooks.EventBeforeLLMCall,
		Payload: map[string]any{"iteration": iter, "message_count": len(messages)},
	})

	req := llm.ChatRequest{
		Model:        modelName,
		SystemPrompt: systemPrompt,
		Messages:     messages,
		Tools:        toolDefs,
	}

	var resp *llm.ChatResponse
	if stream && tokenCh != nil {
		resp, err = a.doStreamingCall(ctx, req, tokenCh)
	} else {
		resp, err = a.llm.Chat(ctx, req)
	}
	if err != nil {
		a.hooks.Emit(ctx, hooks.Event{
			Type:    hooks.EventError,
			Payload: map[string]any{"error": err.Error()},
		})
		return nil, fmt.Errorf("LLM call: %w", err)
	}

	a.hooks.Emit(ctx, hooks.Event{
		Type: hooks.EventAfterLLMCall,
		Payload: map[string]any{
			"finish_reason":     resp.FinishReason,
			"tool_call_count":   len(resp.ToolCalls),
			"prompt_tokens":     resp.Usage.PromptTokens,
			"completion_tokens": resp.Usage.CompletionTokens,
		},
	})

	return resp, nil
}

// processToolCalls records the assistant message, expands tool schemas, and
// executes each tool call, recording results to the tape.
func (a *AgentLoop) processToolCalls(ctx context.Context, resp *llm.ChatResponse) {
	a.appendMessage(llm.RoleAssistant, resp.Content, resp.ToolCalls, "")

	// Auto-expand any tool the model calls, so the next iteration
	// sends the full parameter schema (progressive disclosure).
	for _, tc := range resp.ToolCalls {
		a.tools.Expand(tc.Name)
	}
	if resp.Content != "" {
		a.tools.ExpandHints(resp.Content)
	}

	for _, tc := range resp.ToolCalls {
		toolResult := a.executeTool(ctx, tc)
		a.appendToolResult(tc.ID, tc.Name, toolResult)
	}
}

// processAssistantResponse handles the final answer: runs any embedded comma
// commands, records the response to the tape, and returns the content.
func (a *AgentLoop) processAssistantResponse(ctx context.Context, resp *llm.ChatResponse) string {
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
			cmdResult, _ := a.handleCommand(ctx, route)
			content += "\n" + cmdResult
		}
	}

	a.appendMessage(llm.RoleAssistant, content, nil, "")
	return content
}

// doStreamingCall performs a streaming LLM call, sending content tokens to tokenCh,
// and returns the assembled full response.
func (a *AgentLoop) doStreamingCall(ctx context.Context, req llm.ChatRequest, tokenCh chan<- string) (*llm.ChatResponse, error) {
	eventCh, err := a.llm.ChatStream(ctx, req)
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
			// Assembled below.
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
func (a *AgentLoop) executeTool(ctx context.Context, tc llm.ToolCall) string {
	// Before tool exec hook — can block execution.
	if err := a.hooks.Emit(ctx, hooks.Event{
		Type: hooks.EventBeforeToolExec,
		Payload: map[string]any{
			"tool_name": tc.Name,
			"tool_id":   tc.ID,
			"arguments": tc.Arguments,
		},
	}); err != nil {
		return "Tool execution blocked: " + err.Error()
	}

	result, err := a.tools.Execute(ctx, tc.Name, tc.Arguments)
	if err != nil {
		result = "Error: " + err.Error()
	}

	a.hooks.Emit(ctx, hooks.Event{
		Type: hooks.EventAfterToolExec,
		Payload: map[string]any{
			"tool_name": tc.Name,
			"tool_id":   tc.ID,
			"result":    truncateForLog(result, 500),
		},
	})

	return result
}

// handleCommand dispatches an internal or shell comma-command.
func (a *AgentLoop) handleCommand(ctx context.Context, route router.RouteResult) (string, error) {
	switch route.Kind {
	case router.CommandInternal:
		return a.handleInternalCommand(ctx, route.Command, route.Args)
	case router.CommandShell:
		// Shell commands are executed via the shell_exec tool.
		args, _ := json.Marshal(map[string]string{"command": route.Command})
		result, err := a.tools.Execute(ctx, "shell_exec", string(args))
		if err != nil {
			return "Error: " + err.Error(), nil
		}
		return result, nil
	}
	return "", nil
}

// handleInternalCommand processes built-in comma-commands.
func (a *AgentLoop) handleInternalCommand(_ context.Context, cmd, args string) (string, error) {
	switch cmd {
	case "help":
		return a.helpText(), nil

	case "quit":
		return "", ErrQuit

	case "tape.info":
		info := a.tape.Info()
		return fmt.Sprintf("Tape: %s\nEntries: %d | Anchors: %d | Since last anchor: %d",
			info.FilePath, info.TotalEntries, info.AnchorCount, info.EntriesSinceAnchor), nil

	case "tape.search":
		if args == "" {
			return "Usage: ,tape.search <query>", nil
		}
		results, err := a.tape.Search(args)
		if err != nil {
			return "Error: " + err.Error(), nil
		}
		if len(results) == 0 {
			return "No matches found.", nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Found %d matches:\n", len(results))
		for _, e := range results {
			data, _ := json.Marshal(e.Payload)
			fmt.Fprintf(&b, "  [%s] %s: %s\n", e.Kind, e.Timestamp.Format(time.RFC3339), truncateForLog(string(data), 100))
		}
		return b.String(), nil

	case "tools":
		return a.tools.List(), nil

	case "skills":
		list := a.tools.List()
		// Extract just the skills section.
		if idx := strings.Index(list, "Skills"); idx >= 0 {
			return list[idx:], nil
		}
		return "No skills registered.", nil

	case "model":
		if args == "" {
			return fmt.Sprintf("Current model: %s (provider: %s)", a.config.Model, a.llm.Provider()), nil
		}
		// Model switching would require creating a new client — for now just report.
		return fmt.Sprintf("Model switching is not yet supported. Current: %s", a.config.Model), nil

	case "anchor":
		label := args
		if label == "" {
			label = "manual"
		}
		if err := a.tape.AddAnchor(label); err != nil {
			return "Error: " + err.Error(), nil
		}
		return fmt.Sprintf("Anchor added: %s", label), nil

	default:
		return fmt.Sprintf("Unknown command: %s. Type ,help for available commands.", cmd), nil
	}
}

func (a *AgentLoop) helpText() string {
	return `Available commands:
  ,help              Show this help message
  ,quit              Exit golem
  ,tape.info         Show tape statistics
  ,tape.search <q>   Search tape history
  ,tools             List registered tools
  ,skills            List discovered skills
  ,model [name]      Show or change current model
  ,anchor [label]    Add a tape anchor (context boundary)
  ,<command>         Execute a shell command (e.g., ,ls -la)`
}

// buildSystemPrompt constructs the system prompt for LLM calls.
func (a *AgentLoop) buildSystemPrompt() string {
	var b strings.Builder

	b.WriteString("You are golem, a helpful coding assistant.\n\n")

	// Workspace context.
	if wd, err := os.Getwd(); err == nil {
		fmt.Fprintf(&b, "Working directory: %s\n", wd)
	}
	fmt.Fprintf(&b, "Current time: %s\n\n", time.Now().Format(time.RFC3339))

	b.WriteString("When you need to perform actions, use the available tools. ")
	b.WriteString("Always explain what you're doing before using tools.\n\n")

	// Custom system prompt: prefer per-agent config, fall back to workspace file.
	switch {
	case a.config.SystemPrompt != "":
		b.WriteString(a.config.SystemPrompt)
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
func (a *AgentLoop) appendMessage(role llm.Role, content string, toolCalls []llm.ToolCall, senderID string) {
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

	a.tape.Append(tape.TapeEntry{
		Kind:    tape.KindMessage,
		Payload: payload,
	})
}

// appendToolResult records a tool result to the tape with proper metadata.
func (a *AgentLoop) appendToolResult(toolCallID, toolName, result string) {
	a.tape.Append(tape.TapeEntry{
		Kind: tape.KindMessage,
		Payload: map[string]any{
			"role":         string(llm.RoleTool),
			"content":      result,
			"tool_call_id": toolCallID,
			"name":         toolName,
		},
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
