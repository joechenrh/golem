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

func TestLooksLikePlan(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"english plan", "I'll read the file and check for errors.", true},
		{"english let me", "Let me look into this.", true},
		{"english answer", "The answer is 42.", false},
		{"chinese plan", "我来看一下这个文件。", true},
		{"chinese plan rang", "让我检查一下。", true},
		{"chinese greeting", "你好！我是 Golem，你的助手。你现在想让我帮你做什么？", false},
		{"chinese answer", "这个问题的答案是42。", false},
		{"intent buried deep", strings.Repeat("正常内容。", 50) + "让我看看", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := looksLikePlan(tt.content); got != tt.want {
				t.Errorf("looksLikePlan() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNudgeMessage(t *testing.T) {
	en := nudgeMessage("I'll read the file now.")
	if !strings.Contains(en, "Don't") {
		t.Errorf("English nudge = %q", en)
	}

	cn := nudgeMessage("我来看一下这个文件的内容，然后分析。")
	if !strings.Contains(cn, "工具") {
		t.Errorf("Chinese nudge = %q", cn)
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

func TestBuildSystemPromptPersona(t *testing.T) {
	agent := newTestAgent(t, &mockLLMClient{})
	agent.config.Persona = &config.Persona{
		Soul:     "You are Dwight, a research brain.",
		Identity: "Name: Dwight\nEmoji: magnifier",
		User:     "Name: Alice\nTimezone: UTC",
		Agents:   "Always cite sources.",
		Memory:   "User prefers short answers.",
	}

	prompt := agent.buildSystemPrompt()

	// Layer 1: Identity.
	if !strings.Contains(prompt, "You are Dwight") {
		t.Errorf("prompt missing SOUL.md content")
	}
	if !strings.Contains(prompt, "Name: Dwight") {
		t.Errorf("prompt missing IDENTITY.md content")
	}
	if !strings.Contains(prompt, "Name: Alice") {
		t.Errorf("prompt missing USER.md content")
	}

	// Layer 2: Operations.
	if !strings.Contains(prompt, "Always cite sources.") {
		t.Errorf("prompt missing AGENTS.md content")
	}
	if !strings.Contains(prompt, "use the available tools immediately") {
		t.Errorf("prompt missing built-in tool-use instructions")
	}

	// Layer 3: Knowledge.
	if !strings.Contains(prompt, "MEMORY.md") {
		t.Errorf("prompt missing memory system description")
	}
	if !strings.Contains(prompt, "User prefers short answers.") {
		t.Errorf("prompt missing MEMORY.md content")
	}

	// Environment.
	if !strings.Contains(prompt, "Working directory") {
		t.Errorf("prompt missing environment info")
	}

	// Should NOT contain legacy identity.
	if strings.Contains(prompt, "You are golem") {
		t.Errorf("persona prompt should not contain legacy identity")
	}
}

func TestBuildSystemPromptPersonaMinimal(t *testing.T) {
	agent := newTestAgent(t, &mockLLMClient{})
	agent.config.Persona = &config.Persona{
		Soul: "You are a minimal agent.",
	}

	prompt := agent.buildSystemPrompt()
	if !strings.Contains(prompt, "You are a minimal agent.") {
		t.Errorf("prompt missing SOUL.md content")
	}
	// Optional sections should not have empty headers with no content.
	if strings.Contains(prompt, "User Profile") {
		t.Errorf("prompt should not contain User Profile header when USER.md is empty")
	}
	if strings.Contains(prompt, "Current Memory") {
		t.Errorf("prompt should not contain Current Memory header when MEMORY.md is empty")
	}
}

// ── SessionManager Test Helpers ──────────────────────────────────

func newTestSessionFactory(t *testing.T, tapeDir string) SessionFactory {
	t.Helper()
	return SessionFactory{
		LLMClient: &mockLLMClient{
			responses: []*llm.ChatResponse{
				{Content: "ok", FinishReason: "stop"},
			},
		},
		Config: &config.Config{
			Model:           "openai:gpt-4o",
			MaxToolIter:     15,
			MaxSessions:     100,
			TapeDir:         tapeDir,
			ContextStrategy: "anchor",
		},
		Logger:          zap.NewNop(),
		ToolFactory:     func() *tools.Registry { return tools.NewRegistry() },
		ContextStrategy: "anchor",
		AgentName:       "test",
	}
}

// ── SessionManager Tests ─────────────────────────────────────────

func TestSessionManager_GetOrCreate_ErrorPath(t *testing.T) {
	// Use a nonexistent directory so tape.NewFileStore will fail.
	factory := newTestSessionFactory(t, "/nonexistent/path/that/does/not/exist")
	logger := zap.NewNop()
	sm := NewSessionManager(factory, logger)

	sess, err := sm.GetOrCreate("chan-error")
	if err == nil {
		t.Fatal("expected error from GetOrCreate with invalid tape dir, got nil")
	}
	if sess != nil {
		t.Errorf("expected nil session on error, got %v", sess)
	}
	if sm.Len() != 0 {
		t.Errorf("session map should be empty after failed create, got %d", sm.Len())
	}
}

func TestSessionManager_GetOrCreate_SuccessPath(t *testing.T) {
	dir := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(dir)
	factory := newTestSessionFactory(t, resolved)
	logger := zap.NewNop()
	sm := NewSessionManager(factory, logger)

	sess, err := sm.GetOrCreate("chan-ok")
	if err != nil {
		t.Fatalf("GetOrCreate failed: %v", err)
	}
	if sess == nil {
		t.Fatal("expected non-nil session, got nil")
	}
	if sm.Len() != 1 {
		t.Errorf("session count = %d, want 1", sm.Len())
	}

	// Calling again with the same ID should return the same session.
	sess2, err := sm.GetOrCreate("chan-ok")
	if err != nil {
		t.Fatalf("GetOrCreate (second call) failed: %v", err)
	}
	if sess2 != sess {
		t.Error("expected same session instance for same channel ID")
	}
	if sm.Len() != 1 {
		t.Errorf("session count = %d, want 1 (no duplicate)", sm.Len())
	}
}
