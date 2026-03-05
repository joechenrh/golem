package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/joechenrh/golem/internal/channel"
	"github.com/joechenrh/golem/internal/config"
	"github.com/joechenrh/golem/internal/ctxmgr"
	"github.com/joechenrh/golem/internal/hooks"
	"github.com/joechenrh/golem/internal/llm"
	"github.com/joechenrh/golem/internal/tape"
	"github.com/joechenrh/golem/internal/tools"
)

// ── Mock LLM Client ──────────────────────────────────────────────

type mockLLMClient struct {
	responses []*llm.ChatResponse
	callCount int
}

func (m *mockLLMClient) Provider() llm.Provider { return llm.ProviderOpenAI }

func (m *mockLLMClient) Chat(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	if m.callCount >= len(m.responses) {
		return &llm.ChatResponse{Content: "no more responses", FinishReason: "stop"}, nil
	}
	resp := m.responses[m.callCount]
	m.callCount++
	return resp, nil
}

func (m *mockLLMClient) ChatStream(_ context.Context, req llm.ChatRequest) (<-chan llm.StreamEvent, error) {
	resp, err := m.Chat(context.Background(), req)
	if err != nil {
		return nil, err
	}

	ch := make(chan llm.StreamEvent, 10)
	go func() {
		defer close(ch)
		if resp.Content != "" {
			ch <- llm.StreamEvent{Type: llm.StreamContentDelta, Content: resp.Content}
		}
		for _, tc := range resp.ToolCalls {
			ch <- llm.StreamEvent{
				Type: llm.StreamToolCallDelta,
				ToolCall: &llm.ToolCallDelta{
					ID:        tc.ID,
					Name:      tc.Name,
					Arguments: tc.Arguments,
				},
			}
		}
		ch <- llm.StreamEvent{Type: llm.StreamDone}
	}()
	return ch, nil
}

// ── Mock Tool ────────────────────────────────────────────────────

type mockTool struct {
	name   string
	result string
}

func (m *mockTool) Name() string                { return m.name }
func (m *mockTool) Description() string         { return "mock tool" }
func (m *mockTool) FullDescription() string     { return "mock tool for testing" }
func (m *mockTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (m *mockTool) Execute(_ context.Context, _ string) (string, error) {
	return m.result, nil
}

// ── Test Helpers ─────────────────────────────────────────────────

func newTestAgent(t *testing.T, client *mockLLMClient, extraTools ...tools.Tool) *Session {
	t.Helper()

	dir := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(dir)
	tapePath := filepath.Join(resolved, "tape.jsonl")
	store, err := tape.NewFileStore(tapePath)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	strategy, _ := ctxmgr.NewContextStrategy("anchor")
	registry := tools.NewRegistry()
	for _, tool := range extraTools {
		registry.Register(tool)
	}

	logger := zap.NewNop()
	bus := hooks.NewBus(logger)

	cfg := &config.Config{
		Model:       "openai:gpt-4o",
		MaxToolIter: 15,
	}

	return NewSession(client, registry, store, strategy, bus, cfg, logger)
}

func cliMsg(text string) channel.IncomingMessage {
	return channel.IncomingMessage{
		ChannelID:   "cli",
		ChannelName: "cli",
		Text:        text,
	}
}

// ── Tests ────────────────────────────────────────────────────────

func TestHandleInput_SimpleResponse(t *testing.T) {
	client := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{Content: "Hello! 2+2 is 4.", FinishReason: "stop"},
		},
	}
	agent := newTestAgent(t, client)

	result, err := agent.HandleInput(context.Background(), cliMsg("What is 2+2?"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if result != "Hello! 2+2 is 4." {
		t.Errorf("result = %q", result)
	}

	// Verify tape has entries.
	info := agent.tape.Info()
	if info.TotalEntries < 2 {
		t.Errorf("tape entries = %d, want >= 2", info.TotalEntries)
	}
}

func TestHandleInput_EmptyResponseRetry(t *testing.T) {
	client := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{Content: "", FinishReason: "stop"},
			{Content: "  ", FinishReason: "stop"},
			{Content: "Here is the actual answer.", FinishReason: "stop"},
		},
	}
	agent := newTestAgent(t, client)

	result, err := agent.HandleInput(context.Background(), cliMsg("Hello"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if result != "Here is the actual answer." {
		t.Errorf("result = %q, want non-empty answer", result)
	}
	if client.callCount != 3 {
		t.Errorf("callCount = %d, want 3 (two retries + success)", client.callCount)
	}
}

func TestHandleInput_WithToolCalls(t *testing.T) {
	client := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{
				Content:      "",
				FinishReason: "tool_calls",
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "test_tool", Arguments: `{"input":"test"}`},
				},
			},
			{Content: "The tool returned: mock output", FinishReason: "stop"},
		},
	}
	agent := newTestAgent(t, client, &mockTool{name: "test_tool", result: "mock output"})

	result, err := agent.HandleInput(context.Background(), cliMsg("Use the tool"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if !strings.Contains(result, "mock output") {
		t.Errorf("result = %q, want to contain 'mock output'", result)
	}
	if client.callCount != 2 {
		t.Errorf("LLM called %d times, want 2", client.callCount)
	}
}

func TestHandleInput_MultipleToolCalls(t *testing.T) {
	client := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{
				FinishReason: "tool_calls",
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "tool_a", Arguments: `{}`},
					{ID: "tc2", Name: "tool_b", Arguments: `{}`},
				},
			},
			{Content: "Done with both tools.", FinishReason: "stop"},
		},
	}
	agent := newTestAgent(t, client,
		&mockTool{name: "tool_a", result: "result a"},
		&mockTool{name: "tool_b", result: "result b"},
	)

	result, err := agent.HandleInput(context.Background(), cliMsg("Use both tools"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if result != "Done with both tools." {
		t.Errorf("result = %q", result)
	}
}

func TestHandleInput_ToolCallLimit(t *testing.T) {
	// Create a client that always returns tool calls.
	infiniteClient := &mockLLMClient{}
	for range 20 {
		infiniteClient.responses = append(infiniteClient.responses, &llm.ChatResponse{
			FinishReason: "tool_calls",
			ToolCalls:    []llm.ToolCall{{ID: "tc", Name: "test_tool", Arguments: `{}`}},
		})
	}
	agent := newTestAgent(t, infiniteClient, &mockTool{name: "test_tool", result: "ok"})
	agent.config.MaxToolIter = 3

	result, err := agent.HandleInput(context.Background(), cliMsg("infinite loop"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if !strings.Contains(result, "limit reached") {
		t.Errorf("result = %q, want tool limit message", result)
	}
}

func TestHandleInput_InternalCommand_Help(t *testing.T) {
	agent := newTestAgent(t, &mockLLMClient{})

	result, err := agent.HandleInput(context.Background(), cliMsg(":help"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if !strings.Contains(result, ":help") {
		t.Errorf("help output should list commands: %s", result)
	}
	if !strings.Contains(result, ":quit") {
		t.Errorf("help output should mention quit: %s", result)
	}
}

func TestHandleInput_InternalCommand_Quit(t *testing.T) {
	agent := newTestAgent(t, &mockLLMClient{})

	_, err := agent.HandleInput(context.Background(), cliMsg(":quit"))
	if err != ErrQuit {
		t.Errorf("err = %v, want ErrQuit", err)
	}
}

func TestHandleInput_InternalCommand_TapeInfo(t *testing.T) {
	agent := newTestAgent(t, &mockLLMClient{})

	result, err := agent.HandleInput(context.Background(), cliMsg(":tape.info"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if !strings.Contains(result, "Entries:") {
		t.Errorf("result = %q, want tape info", result)
	}
}

func TestHandleInput_InternalCommand_Reset(t *testing.T) {
	agent := newTestAgent(t, &mockLLMClient{})

	result, err := agent.HandleInput(context.Background(), cliMsg(":reset test-anchor"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if !strings.Contains(result, "test-anchor") {
		t.Errorf("result = %q", result)
	}

	// Verify anchor exists.
	anchor, _ := agent.tape.LastAnchor()
	if anchor == nil {
		t.Fatal("anchor not found in tape")
	}
}

func TestHandleInput_InternalCommand_Tools(t *testing.T) {
	agent := newTestAgent(t, &mockLLMClient{}, &mockTool{name: "test_tool", result: "ok"})

	result, err := agent.HandleInput(context.Background(), cliMsg(":tools"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if !strings.Contains(result, "test_tool") {
		t.Errorf("result = %q, want to list test_tool", result)
	}
}

func TestHandleInput_InternalCommand_Model(t *testing.T) {
	agent := newTestAgent(t, &mockLLMClient{})

	result, err := agent.HandleInput(context.Background(), cliMsg(":model"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if !strings.Contains(result, "openai:gpt-4o") {
		t.Errorf("result = %q, want current model", result)
	}
}

func TestHandleInputStream_Simple(t *testing.T) {
	client := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{Content: "Streamed response!", FinishReason: "stop"},
		},
	}
	agent := newTestAgent(t, client)

	tokenCh := make(chan string, 100)
	var tokens []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		for tok := range tokenCh {
			tokens = append(tokens, tok)
		}
	}()

	err := agent.HandleInputStream(context.Background(), cliMsg("Hello"), tokenCh)
	close(tokenCh)
	<-done

	if err != nil {
		t.Fatalf("HandleInputStream: %v", err)
	}
	joined := strings.Join(tokens, "")
	if joined != "Streamed response!" {
		t.Errorf("streamed = %q, want %q", joined, "Streamed response!")
	}
}

func TestHandleInputStream_Command(t *testing.T) {
	agent := newTestAgent(t, &mockLLMClient{})

	tokenCh := make(chan string, 100)
	var tokens []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		for tok := range tokenCh {
			tokens = append(tokens, tok)
		}
	}()

	err := agent.HandleInputStream(context.Background(), cliMsg(":help"), tokenCh)
	close(tokenCh)
	<-done

	if err != nil {
		t.Fatalf("HandleInputStream: %v", err)
	}
	result := strings.Join(tokens, "")
	if !strings.Contains(result, ":help") {
		t.Errorf("result = %q, should contain help text", result)
	}
}

func TestHandleInput_HookBlocking(t *testing.T) {
	client := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{
				FinishReason: "tool_calls",
				ToolCalls:    []llm.ToolCall{{ID: "tc1", Name: "test_tool", Arguments: `{}`}},
			},
			{Content: "Tool was blocked.", FinishReason: "stop"},
		},
	}

	agent := newTestAgent(t, client, &mockTool{name: "test_tool", result: "should not see this"})

	// Register a blocking hook.
	agent.hooks.Register(&blockingHook{})

	result, err := agent.HandleInput(context.Background(), cliMsg("try the tool"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	// The tool should have been blocked.
	if strings.Contains(result, "should not see this") {
		t.Error("tool should have been blocked by hook")
	}
}

type blockingHook struct{}

func (h *blockingHook) Name() string { return "blocker" }
func (h *blockingHook) Handle(_ context.Context, event hooks.Event) error {
	if event.Type == hooks.EventBeforeToolExec {
		return fmt.Errorf("blocked by safety hook")
	}
	return nil
}

func TestHandleInput_InternalCommand_TapeSearch(t *testing.T) {
	client := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{Content: "Hello world response", FinishReason: "stop"},
		},
	}
	agent := newTestAgent(t, client)

	// Add a message to search through.
	agent.HandleInput(context.Background(), cliMsg("Hello world"))

	result, err := agent.HandleInput(context.Background(), cliMsg(":tape.search hello"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if !strings.Contains(result, "matches") {
		t.Errorf("result = %q, want search results", result)
	}
}

func TestHandleInput_InternalCommand_TapeSearchEmpty(t *testing.T) {
	agent := newTestAgent(t, &mockLLMClient{})

	result, err := agent.HandleInput(context.Background(), cliMsg(":tape.search"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if !strings.Contains(result, "Usage:") {
		t.Errorf("result = %q, want usage hint", result)
	}
}

func TestHandleInput_InternalCommand_TapeSearchNoMatch(t *testing.T) {
	agent := newTestAgent(t, &mockLLMClient{})

	result, err := agent.HandleInput(context.Background(), cliMsg(":tape.search nonexistent"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if !strings.Contains(result, "No matches") {
		t.Errorf("result = %q, want no matches", result)
	}
}

func TestHandleInput_InternalCommand_Skills(t *testing.T) {
	agent := newTestAgent(t, &mockLLMClient{})

	result, err := agent.HandleInput(context.Background(), cliMsg(":skills"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if !strings.Contains(result, "No skills") {
		t.Errorf("result = %q, want no skills message", result)
	}
}

func TestHandleInput_InternalCommand_ResetDefaultLabel(t *testing.T) {
	agent := newTestAgent(t, &mockLLMClient{})

	result, err := agent.HandleInput(context.Background(), cliMsg(":reset"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if !strings.Contains(result, "manual") {
		t.Errorf("result = %q, want default 'manual' label", result)
	}
}

func TestHandleInput_InternalCommand_ModelWithArg(t *testing.T) {
	agent := newTestAgent(t, &mockLLMClient{})

	result, err := agent.HandleInput(context.Background(), cliMsg(":model openai:gpt-3.5"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if !strings.Contains(result, "not yet supported") {
		t.Errorf("result = %q, want unsupported message", result)
	}
}

func TestHandleInput_ShellCommand(t *testing.T) {
	agent := newTestAgent(t, &mockLLMClient{})

	// :foobar is routed as a shell command (not internal).
	// Without shell_exec tool registered, it should return an error.
	result, err := agent.HandleInput(context.Background(), cliMsg(":echo hello"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if !strings.Contains(result, "Error:") {
		t.Errorf("result = %q, want error (shell_exec tool not registered)", result)
	}
}

func TestTruncateForLog(t *testing.T) {
	long := strings.Repeat("a", 200)
	truncated := truncateForLog(long, 50)
	if len(truncated) != 53 { // 50 + "..."
		t.Errorf("truncated length = %d, want 53", len(truncated))
	}
	if !strings.HasSuffix(truncated, "...") {
		t.Error("truncated string should end with ...")
	}

	short := "hello"
	if truncateForLog(short, 50) != "hello" {
		t.Error("short strings should not be truncated")
	}
}

func TestBuildSystemPrompt(t *testing.T) {
	agent := newTestAgent(t, &mockLLMClient{})

	prompt := agent.buildSystemPrompt()
	if !strings.Contains(prompt, "golem") {
		t.Errorf("prompt should contain identity: %s", prompt)
	}
	if !strings.Contains(prompt, "Working directory") {
		t.Errorf("prompt should contain workspace context: %s", prompt)
	}
}
