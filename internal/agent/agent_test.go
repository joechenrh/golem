package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"

	"github.com/joechenrh/golem/internal/channel"
	"github.com/joechenrh/golem/internal/config"
	"github.com/joechenrh/golem/internal/ctxmgr"
	"github.com/joechenrh/golem/internal/hooks"
	"github.com/joechenrh/golem/internal/llm"
	"github.com/joechenrh/golem/internal/stringutil"
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

	return NewSession(client, nil, registry, store, strategy, bus, cfg, logger)
}

func newTestAgentWithClassifier(t *testing.T, client, classifierClient *mockLLMClient, extraTools ...tools.Tool) *Session {
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
		Model:           "openai:gpt-4o",
		ClassifierModel: "openai:gpt-4o-mini",
		MaxToolIter:     15,
	}

	return NewSession(client, classifierClient, registry, store, strategy, bus, cfg, logger)
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

func TestIsAmbiguousResponse(t *testing.T) {
	dir := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(dir)
	storeWithTools, err := tape.NewFileStore(filepath.Join(resolved, "tape-tools.jsonl"))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	storeWithTools.Append(tape.TapeEntry{
		Kind:    tape.KindMessage,
		Payload: json.RawMessage(`{"role":"tool","content":"ok","tool_call_id":"tc1","name":"test"}`),
	})
	storeEmpty, err := tape.NewFileStore(filepath.Join(resolved, "tape-empty.jsonl"))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	tests := []struct {
		name    string
		content string
		store   tape.Store
		want    bool
	}{
		{"non-empty with tool history", "可以。你想查哪方面的新闻？", storeWithTools, true},
		{"long response with tool history", "I'll read the file and check for errors.", storeWithTools, true},
		{"empty content", "", storeEmpty, false},
		{"whitespace only", "   ", storeWithTools, false},
		{"no tool history", "好的", storeEmpty, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAmbiguousResponse(tt.content, tt.store); got != tt.want {
				t.Errorf("isAmbiguousResponse(%q) = %v, want %v", tt.content, got, tt.want)
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

func TestTruncate(t *testing.T) {
	long := strings.Repeat("a", 200)
	truncated := stringutil.Truncate(long, 50)
	if len(truncated) != 53 { // 50 + "..."
		t.Errorf("truncated length = %d, want 53", len(truncated))
	}
	if !strings.HasSuffix(truncated, "...") {
		t.Error("truncated string should end with ...")
	}

	short := "hello"
	if stringutil.Truncate(short, 50) != "hello" {
		t.Error("short strings should not be truncated")
	}
}

func TestParseClassifierResponse(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantDec  string
		wantTask string
		wantOK   bool
	}{
		{
			name:    "nudge decision",
			body:    `{"decision":"nudge"}`,
			wantDec: "nudge", wantTask: "", wantOK: true,
		},
		{
			name:    "accept decision",
			body:    `{"decision":"accept"}`,
			wantDec: "accept", wantTask: "", wantOK: true,
		},
		{
			name:     "stuck with summary",
			body:     `{"decision":"stuck","task_summary":"Rewrite the doc in English"}`,
			wantDec:  "stuck",
			wantTask: "Rewrite the doc in English",
			wantOK:   true,
		},
		{
			name:   "invalid json",
			body:   `not json`,
			wantOK: false,
		},
		{
			name:   "unknown decision",
			body:   `{"decision":"unknown"}`,
			wantOK: false,
		},
		{
			name:    "whitespace around json",
			body:    `  {"decision":"accept"}  `,
			wantDec: "accept", wantOK: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec, task, ok := parseClassifierResponse(tt.body)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if dec != tt.wantDec {
				t.Errorf("decision = %q, want %q", dec, tt.wantDec)
			}
			if task != tt.wantTask {
				t.Errorf("task_summary = %q, want %q", task, tt.wantTask)
			}
		})
	}
}

func TestTaskReminderMessage(t *testing.T) {
	en := taskReminderMessage("Rewrite the doc in English", "Hello, I will rewrite the doc.")
	if !strings.Contains(en, "Rewrite the doc in English") {
		t.Errorf("English reminder missing task: %q", en)
	}
	if !strings.Contains(en, "stuck") {
		t.Errorf("English reminder missing stuck indicator: %q", en)
	}

	cn := taskReminderMessage("用英语重写文档", "好的，我来重写一下这个文档。这个文档需要用中文来完成。")
	if !strings.Contains(cn, "用英语重写文档") {
		t.Errorf("Chinese reminder missing task: %q", cn)
	}
	if !strings.Contains(cn, "卡住") {
		t.Errorf("Chinese reminder missing stuck indicator: %q", cn)
	}
}

func TestSanitizeTaskSummary(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain text unchanged",
			in:   "修改文档顺序",
			want: "修改文档顺序",
		},
		{
			name: "strips URL",
			in:   "https://example.com/very/long/path?token=abc123 这个文档继续修改，顺序还是不对",
			want: "这个文档继续修改，顺序还是不对",
		},
		{
			name: "strips multiple URLs",
			in:   "check https://a.com and https://b.com please",
			want: "check  and  please",
		},
		{
			name: "truncates long text",
			in:   strings.Repeat("x", 300),
			want: strings.Repeat("x", 200) + "...",
		},
		{
			name: "empty after URL strip",
			in:   "https://example.com/path",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeTaskSummary(tt.in)
			if got != tt.want {
				t.Errorf("sanitizeTaskSummary() = %q, want %q", got, tt.want)
			}
		})
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
		Soul:   "You are Dwight, a research brain.",
		User:   "Name: Alice\nTimezone: UTC",
		Agents: "Always cite sources.",
		Memory: "User prefers short answers.",
	}

	prompt := agent.buildSystemPrompt()

	// Layer 1: Identity.
	if !strings.Contains(prompt, "You are Dwight") {
		t.Errorf("prompt missing SOUL.md content")
	}
	if !strings.Contains(prompt, "Name: Alice") {
		t.Errorf("prompt missing USER.md content")
	}

	// Layer 2: Operations.
	if !strings.Contains(prompt, "Always cite sources.") {
		t.Errorf("prompt missing AGENTS.md content")
	}
	if !strings.Contains(prompt, "call tools directly") {
		t.Errorf("prompt missing built-in tool-use instructions")
	}

	// Layer 3: Knowledge — persona files reference.
	if !strings.Contains(prompt, "persona_self") {
		t.Errorf("prompt missing persona_self tool reference")
	}
	if !strings.Contains(prompt, "MEMORY.md") {
		t.Errorf("prompt missing MEMORY.md description")
	}
	if !strings.Contains(prompt, "SOUL.md") {
		t.Errorf("prompt missing SOUL.md description")
	}
	if !strings.Contains(prompt, "AGENTS.md") {
		t.Errorf("prompt missing AGENTS.md description")
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

func TestSessionManager_GetOrCreate_ConcurrentStress(t *testing.T) {
	dir := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(dir)
	factory := newTestSessionFactory(t, resolved)
	logger := zap.NewNop()
	sm := NewSessionManager(factory, logger)

	const numUnique = 50
	const numShared = 50
	sharedID := "shared-channel"

	var wg sync.WaitGroup

	// Track errors from goroutines.
	errCh := make(chan error, numUnique+numShared)

	// 50 goroutines with unique channel IDs.
	for i := range numUnique {
		wg.Add(1)
		go func() {
			defer wg.Done()
			chID := fmt.Sprintf("unique-%d", i)
			sess, err := sm.GetOrCreate(chID)
			if err != nil {
				errCh <- fmt.Errorf("unique %s: %w", chID, err)
				return
			}
			if sess == nil {
				errCh <- fmt.Errorf("unique %s: nil session", chID)
			}
		}()
	}

	// 50 goroutines with the SAME channel ID.
	sessions := make([]*Session, numShared)
	var mu sync.Mutex
	for i := range numShared {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sess, err := sm.GetOrCreate(sharedID)
			if err != nil {
				errCh <- fmt.Errorf("shared %d: %w", i, err)
				return
			}
			if sess == nil {
				errCh <- fmt.Errorf("shared %d: nil session", i)
				return
			}
			mu.Lock()
			sessions[i] = sess
			mu.Unlock()
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("goroutine error: %v", err)
	}

	// All goroutines accessing the shared channel should get the same session.
	var firstShared *Session
	for i, s := range sessions {
		if s == nil {
			continue
		}
		if firstShared == nil {
			firstShared = s
		} else if s != firstShared {
			t.Errorf("shared session %d differs from first; expected same instance", i)
		}
	}

	// Total sessions: 50 unique + 1 shared = 51.
	expectedCount := numUnique + 1
	if sm.Len() != expectedCount {
		t.Errorf("session count = %d, want %d", sm.Len(), expectedCount)
	}
}

func TestHandleInput_ClassifierNudge(t *testing.T) {
	// First response is "好的" → classifier says "nudge" → nudge injected.
	// Second response calls a tool.
	// Third response is the final answer.
	client := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{Content: "好的", FinishReason: "stop"},
			{
				FinishReason: "tool_calls",
				ToolCalls:    []llm.ToolCall{{ID: "tc1", Name: "test_tool", Arguments: `{}`}},
			},
			{Content: "Done! Here is the result.", FinishReason: "stop"},
		},
	}
	classifierClient := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{Content: `{"decision":"nudge"}`, FinishReason: "stop"},
			{Content: `{"decision":"accept"}`, FinishReason: "stop"},
		},
	}
	agent := newTestAgentWithClassifier(t, client, classifierClient, &mockTool{name: "test_tool", result: "ok"})

	// Seed tool history so isAmbiguousResponse returns true.
	agent.tape.Append(tape.TapeEntry{
		Kind:    tape.KindMessage,
		Payload: json.RawMessage(`{"role":"tool","content":"prev","tool_call_id":"old","name":"test_tool"}`),
	})

	result, err := agent.HandleInput(context.Background(), cliMsg("帮我查一下"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if strings.Contains(result, "好的") {
		t.Errorf("result should not contain nudged response, got %q", result)
	}
	if !strings.Contains(result, "Done!") {
		t.Errorf("result = %q, want final answer", result)
	}
	// LLM called 3 times: ack → (classifier nudge) → tool call → final.
	if client.callCount != 3 {
		t.Errorf("callCount = %d, want 3", client.callCount)
	}
}

func TestHandleInput_NoClassifier_AcceptsDirectly(t *testing.T) {
	// Without classifier, plan-like responses are accepted as-is.
	client := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{Content: "I'll read the file now.", FinishReason: "stop"},
		},
	}
	agent := newTestAgent(t, client) // no classifier

	result, err := agent.HandleInput(context.Background(), cliMsg("Read the file"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if result != "I'll read the file now." {
		t.Errorf("result = %q, want plan-like response accepted as-is", result)
	}
	if client.callCount != 1 {
		t.Errorf("callCount = %d, want 1 (no nudging without classifier)", client.callCount)
	}
}

func TestHandleInput_ClassifierAcceptsClarification(t *testing.T) {
	// Classifier correctly accepts a clarifying question as a final answer.
	client := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{Content: "可以。你想查哪方面的新闻？", FinishReason: "stop"},
		},
	}
	classifierClient := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{Content: `{"decision":"accept"}`, FinishReason: "stop"},
		},
	}
	agent := newTestAgentWithClassifier(t, client, classifierClient)

	// Seed tool history so classifier is invoked.
	agent.tape.Append(tape.TapeEntry{
		Kind:    tape.KindMessage,
		Payload: json.RawMessage(`{"role":"tool","content":"prev","tool_call_id":"old","name":"test_tool"}`),
	})

	result, err := agent.HandleInput(context.Background(), cliMsg("你能查新闻吗"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if result != "可以。你想查哪方面的新闻？" {
		t.Errorf("result = %q, want clarifying question accepted", result)
	}
	if client.callCount != 1 {
		t.Errorf("callCount = %d, want 1", client.callCount)
	}
}

func TestHandleInputStream_NudgeDoesNotLeak(t *testing.T) {
	// LLM returns a plan-like response first, classifier says "nudge",
	// then the real answer.
	client := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{Content: "I'll read the file now.", FinishReason: "stop"},
			{Content: "The file contains hello world.", FinishReason: "stop"},
		},
	}
	classifierClient := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{Content: `{"decision":"nudge"}`, FinishReason: "stop"},
			{Content: `{"decision":"accept"}`, FinishReason: "stop"},
		},
	}
	agent := newTestAgentWithClassifier(t, client, classifierClient)

	// Seed tool history so classifier is invoked.
	agent.tape.Append(tape.TapeEntry{
		Kind:    tape.KindMessage,
		Payload: json.RawMessage(`{"role":"tool","content":"prev","tool_call_id":"old","name":"test_tool"}`),
	})

	tokenCh := make(chan string, 100)
	var tokens []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		for tok := range tokenCh {
			tokens = append(tokens, tok)
		}
	}()

	err := agent.HandleInputStream(context.Background(), cliMsg("Read the file"), tokenCh)
	close(tokenCh)
	<-done

	if err != nil {
		t.Fatalf("HandleInputStream: %v", err)
	}
	joined := strings.Join(tokens, "")
	if strings.Contains(joined, "I'll read the file") {
		t.Errorf("nudged response leaked to stream: %q", joined)
	}
	if !strings.Contains(joined, "hello world") {
		t.Errorf("final response missing from stream: %q", joined)
	}
}

func TestHandleInputStream_ToolCallsThenFinalAnswer(t *testing.T) {
	// Tool call iteration (no content) then final answer — should stream normally.
	client := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{
				FinishReason: "tool_calls",
				ToolCalls:    []llm.ToolCall{{ID: "tc1", Name: "test_tool", Arguments: `{}`}},
			},
			{Content: "Tool returned: mock output", FinishReason: "stop"},
		},
	}
	agent := newTestAgent(t, client, &mockTool{name: "test_tool", result: "mock output"})

	tokenCh := make(chan string, 100)
	var tokens []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		for tok := range tokenCh {
			tokens = append(tokens, tok)
		}
	}()

	err := agent.HandleInputStream(context.Background(), cliMsg("Use tool"), tokenCh)
	close(tokenCh)
	<-done

	if err != nil {
		t.Fatalf("HandleInputStream: %v", err)
	}
	joined := strings.Join(tokens, "")
	if !strings.Contains(joined, "mock output") {
		t.Errorf("final response missing: %q", joined)
	}
}
