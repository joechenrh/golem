//go:build integration

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"

	"github.com/joechenrh/golem/internal/channel"
	"github.com/joechenrh/golem/internal/config"
	"github.com/joechenrh/golem/internal/ctxmgr"
	"github.com/joechenrh/golem/internal/executor"
	"github.com/joechenrh/golem/internal/fs"
	"github.com/joechenrh/golem/internal/hooks"
	"github.com/joechenrh/golem/internal/llm"
	"github.com/joechenrh/golem/internal/tape"
	"github.com/joechenrh/golem/internal/tools"
	"github.com/joechenrh/golem/internal/tools/builtin"
)

// ── Mock OpenAI-compatible server ──────────────────────────────────

type mockOpenAIServer struct {
	mu        sync.Mutex
	t         *testing.T
	responses []mockResponse
	calls     []json.RawMessage // raw request bodies received
	callIdx   int
}

type mockResponse struct {
	content    string
	toolCalls  []mockToolCall
	statusCode int // 0 means 200
}

type mockToolCall struct {
	id   string
	name string
	args string
}

func newMockOpenAIServer(t *testing.T, responses []mockResponse) (*httptest.Server, *mockOpenAIServer) {
	t.Helper()
	mock := &mockOpenAIServer{t: t, responses: responses}
	srv := httptest.NewServer(http.HandlerFunc(mock.handler))
	t.Cleanup(srv.Close)
	return srv, mock
}

func (m *mockOpenAIServer) handler(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Record the request body.
	var body json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		m.t.Errorf("mock server: decode request: %v", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	m.calls = append(m.calls, body)

	// Check if this is a streaming request.
	var reqObj struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &reqObj)

	if m.callIdx >= len(m.responses) {
		if reqObj.Stream {
			m.writeStreamResponse(w, mockResponse{content: "no more scripted responses"})
		} else {
			m.writeResponse(w, mockResponse{content: "no more scripted responses"})
		}
		return
	}

	resp := m.responses[m.callIdx]
	m.callIdx++

	if resp.statusCode != 0 && resp.statusCode != 200 {
		http.Error(w, `{"error":{"message":"mock error","type":"server_error"}}`, resp.statusCode)
		return
	}

	if reqObj.Stream {
		m.writeStreamResponse(w, resp)
	} else {
		m.writeResponse(w, resp)
	}
}

func (m *mockOpenAIServer) writeResponse(w http.ResponseWriter, resp mockResponse) {
	// Build an OpenAI-format response.
	finishReason := "stop"
	var toolCallsJSON []map[string]any
	if len(resp.toolCalls) > 0 {
		finishReason = "tool_calls"
		for _, tc := range resp.toolCalls {
			toolCallsJSON = append(toolCallsJSON, map[string]any{
				"id":   tc.id,
				"type": "function",
				"function": map[string]string{
					"name":      tc.name,
					"arguments": tc.args,
				},
			})
		}
	}

	message := map[string]any{
		"role":    "assistant",
		"content": resp.content,
	}
	if len(toolCallsJSON) > 0 {
		message["tool_calls"] = toolCallsJSON
	}

	result := map[string]any{
		"choices": []map[string]any{
			{
				"message":       message,
				"finish_reason": finishReason,
			},
		},
		"usage": map[string]int{
			"prompt_tokens":     10,
			"completion_tokens": 5,
			"total_tokens":      15,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// writeStreamResponse writes an SSE-format streaming response.
func (m *mockOpenAIServer) writeStreamResponse(w http.ResponseWriter, resp mockResponse) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)

	// Send content as a single chunk.
	if resp.content != "" {
		chunk := map[string]any{
			"choices": []map[string]any{
				{
					"delta": map[string]any{
						"content": resp.content,
					},
					"finish_reason": nil,
				},
			},
		}
		data, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", data)
		if flusher != nil {
			flusher.Flush()
		}
	}

	// Send [DONE].
	fmt.Fprintf(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func (m *mockOpenAIServer) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// ── Test harness ───────────────────────────────────────────────────

type testHarness struct {
	agent    *Session
	tape     tape.Store
	registry *tools.Registry
	mock     *mockOpenAIServer
}

func newTestHarness(t *testing.T, responses []mockResponse) *testHarness {
	t.Helper()

	srv, mock := newMockOpenAIServer(t, responses)

	// Create LLM client pointing at mock server.
	client, err := llm.NewClient(llm.ProviderOpenAI, "test-key", llm.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	dir := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(dir)

	// Tape store.
	tapeStore, err := tape.NewFileStore(filepath.Join(resolved, "tape.jsonl"))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	// Context strategy.
	strategy, _ := ctxmgr.NewContextStrategy("anchor")

	// Executor & filesystem for shell/file tools.
	workDir := filepath.Join(resolved, "workspace")
	os.MkdirAll(workDir, 0o755)
	exec := executor.NewLocal(workDir)
	filesystem, _ := fs.NewLocalFS(workDir)

	// Tool registry with real tools.
	registry := tools.NewRegistry()
	registry.RegisterAll(
		builtin.NewShellTool(exec, 5_000_000_000), // 5s timeout
		builtin.NewReadFileTool(filesystem),
		builtin.NewWriteFileTool(filesystem),
		builtin.NewListDirectoryTool(filesystem),
	)

	// Hooks and config.
	logger := zap.NewNop()
	bus := hooks.NewBus(logger)

	cfg := &config.Config{
		Model:       "openai:gpt-4o",
		MaxToolIter: 15,
	}

	agentLoop := NewSession(client, registry, tapeStore, strategy, bus, cfg, logger)

	return &testHarness{
		agent:    agentLoop,
		tape:     tapeStore,
		registry: registry,
		mock:     mock,
	}
}

func msg(text string) channel.IncomingMessage {
	return channel.IncomingMessage{
		ChannelID:   "test",
		ChannelName: "test",
		Text:        text,
	}
}

// ── Integration tests ──────────────────────────────────────────────

func TestIntegration_SimpleQA(t *testing.T) {
	h := newTestHarness(t, []mockResponse{
		{content: "The answer is 4."},
	})

	result, err := h.agent.HandleInput(context.Background(), msg("What is 2+2?"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if result != "The answer is 4." {
		t.Errorf("result = %q", result)
	}
	if h.mock.callCount() != 1 {
		t.Errorf("LLM called %d times, want 1", h.mock.callCount())
	}

	// Verify tape has user + assistant entries.
	entries, _ := h.tape.Entries()
	if len(entries) < 2 {
		t.Errorf("tape entries = %d, want >= 2", len(entries))
	}
}

func TestIntegration_SingleToolCall(t *testing.T) {
	h := newTestHarness(t, []mockResponse{
		// First response: tool call to list directory.
		{toolCalls: []mockToolCall{
			{id: "tc1", name: "list_directory", args: `{"path":"."}`},
		}},
		// Second response: final answer referencing tool result.
		{content: "The workspace is empty."},
	})

	result, err := h.agent.HandleInput(context.Background(), msg("What files are in the workspace?"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if result != "The workspace is empty." {
		t.Errorf("result = %q", result)
	}
	if h.mock.callCount() != 2 {
		t.Errorf("LLM called %d times, want 2", h.mock.callCount())
	}

	// Verify tape: user + assistant(tool_call) + tool_result + assistant.
	entries, _ := h.tape.Entries()
	if len(entries) < 4 {
		t.Errorf("tape entries = %d, want >= 4", len(entries))
	}
}

func TestIntegration_MultiStepToolCalls(t *testing.T) {
	h := newTestHarness(t, []mockResponse{
		// Step 1: write a file.
		{toolCalls: []mockToolCall{
			{id: "tc1", name: "write_file", args: `{"path":"hello.txt","content":"hello world"}`},
		}},
		// Step 2: read it back.
		{toolCalls: []mockToolCall{
			{id: "tc2", name: "read_file", args: `{"path":"hello.txt"}`},
		}},
		// Step 3: final answer.
		{content: "File contains: hello world"},
	})

	result, err := h.agent.HandleInput(context.Background(), msg("Write hello.txt then read it"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if !strings.Contains(result, "hello world") {
		t.Errorf("result = %q, want to contain 'hello world'", result)
	}
	if h.mock.callCount() != 3 {
		t.Errorf("LLM called %d times, want 3", h.mock.callCount())
	}
}

func TestIntegration_ToolCallLimit(t *testing.T) {
	// Create responses that always return tool calls.
	var responses []mockResponse
	for i := range 20 {
		responses = append(responses, mockResponse{
			toolCalls: []mockToolCall{
				{id: fmt.Sprintf("tc%d", i), name: "list_directory", args: `{"path":"."}`},
			},
		})
	}

	h := newTestHarness(t, responses)
	h.agent.config.MaxToolIter = 3

	result, err := h.agent.HandleInput(context.Background(), msg("Keep going"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if !strings.Contains(result, "limit reached") {
		t.Errorf("result = %q, want limit message", result)
	}
	// Should have made exactly 3 LLM calls.
	if h.mock.callCount() != 3 {
		t.Errorf("LLM called %d times, want 3", h.mock.callCount())
	}
}

func TestIntegration_ColonCommandBypass(t *testing.T) {
	h := newTestHarness(t, nil) // no responses needed

	// :help should not call LLM.
	result, err := h.agent.HandleInput(context.Background(), msg(":help"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if !strings.Contains(result, ":help") {
		t.Errorf("help result = %q", result)
	}
	if h.mock.callCount() != 0 {
		t.Errorf("LLM called %d times for :help, want 0", h.mock.callCount())
	}

	// :tape.info should not call LLM.
	result, err = h.agent.HandleInput(context.Background(), msg(":tape.info"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if !strings.Contains(result, "Entries:") {
		t.Errorf("tape.info result = %q", result)
	}
	if h.mock.callCount() != 0 {
		t.Errorf("LLM called %d times for :tape.info, want 0", h.mock.callCount())
	}
}

func TestIntegration_Streaming(t *testing.T) {
	h := newTestHarness(t, []mockResponse{
		{content: "Streamed answer!"},
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

	err := h.agent.HandleInputStream(context.Background(), msg("Hello"), tokenCh)
	close(tokenCh)
	<-done

	if err != nil {
		t.Fatalf("HandleInputStream: %v", err)
	}
	joined := strings.Join(tokens, "")
	if joined != "Streamed answer!" {
		t.Errorf("streamed = %q, want %q", joined, "Streamed answer!")
	}
}

func TestIntegration_TapeRecordsConversation(t *testing.T) {
	h := newTestHarness(t, []mockResponse{
		{content: "First answer."},
		{content: "Second answer."},
	})

	// First turn.
	_, err := h.agent.HandleInput(context.Background(), msg("Question 1"))
	if err != nil {
		t.Fatalf("HandleInput 1: %v", err)
	}

	// Second turn.
	_, err = h.agent.HandleInput(context.Background(), msg("Question 2"))
	if err != nil {
		t.Fatalf("HandleInput 2: %v", err)
	}

	// Tape should have: user1 + assistant1 + user2 + assistant2 = 4 entries.
	entries, _ := h.tape.Entries()
	if len(entries) != 4 {
		t.Errorf("tape entries = %d, want 4", len(entries))
	}

	// Verify the sequence of roles.
	expectedRoles := []string{"user", "assistant", "user", "assistant"}
	for i, e := range entries {
		role, _ := e.PayloadMap()["role"].(string)
		if role != expectedRoles[i] {
			t.Errorf("entry %d role = %q, want %q", i, role, expectedRoles[i])
		}
	}
}

func TestIntegration_EmptyResponseRetry(t *testing.T) {
	h := newTestHarness(t, []mockResponse{
		// First response: empty content, no tool calls.
		{content: ""},
		// Second response: also empty (whitespace only).
		{content: "   "},
		// Third response: actual content.
		{content: "Here is your answer."},
	})

	result, err := h.agent.HandleInput(context.Background(), msg("Give me an answer"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if result != "Here is your answer." {
		t.Errorf("result = %q, want %q", result, "Here is your answer.")
	}
	// Should have retried twice before getting the real answer.
	if h.mock.callCount() != 3 {
		t.Errorf("LLM called %d times, want 3", h.mock.callCount())
	}
}

func TestIntegration_NudgeBehavior(t *testing.T) {
	h := newTestHarness(t, []mockResponse{
		// First response: plan-like text that should trigger a nudge.
		{content: "I'll read the directory and then check the files."},
		// After nudge, LLM uses a tool.
		{toolCalls: []mockToolCall{
			{id: "tc1", name: "list_directory", args: `{"path":"."}`},
		}},
		// Final answer after tool result.
		{content: "The workspace is empty."},
	})

	result, err := h.agent.HandleInput(context.Background(), msg("Check the workspace"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if result != "The workspace is empty." {
		t.Errorf("result = %q", result)
	}
	// 3 LLM calls: plan → (nudge) → tool call → final answer.
	if h.mock.callCount() != 3 {
		t.Errorf("LLM called %d times, want 3", h.mock.callCount())
	}

	// Verify the nudge message was injected into the tape.
	entries, _ := h.tape.Entries()
	foundNudge := false
	for _, e := range entries {
		pm := e.PayloadMap()
		content, _ := pm["content"].(string)
		if strings.Contains(content, "use the available tools") {
			foundNudge = true
			break
		}
	}
	if !foundNudge {
		t.Error("nudge message not found in tape")
	}
}

func TestIntegration_SelfCorrectionOnRepeatedToolFailure(t *testing.T) {
	// The tool "read_file" will fail because "nonexistent.txt" doesn't exist.
	// After maxToolFailures (3), a self-correction message should appear.
	var responses []mockResponse
	for i := range 5 {
		responses = append(responses, mockResponse{
			toolCalls: []mockToolCall{
				{id: fmt.Sprintf("tc%d", i), name: "read_file", args: `{"path":"nonexistent.txt"}`},
			},
		})
	}
	// Final answer after self-correction hint.
	responses = append(responses, mockResponse{content: "The file does not exist."})

	h := newTestHarness(t, responses)

	result, err := h.agent.HandleInput(context.Background(), msg("Read nonexistent.txt"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if !strings.Contains(result, "does not exist") {
		t.Errorf("result = %q", result)
	}

	// Verify self-correction message was injected.
	entries, _ := h.tape.Entries()
	foundCorrection := false
	for _, e := range entries {
		pm := e.PayloadMap()
		content, _ := pm["content"].(string)
		if strings.Contains(content, "Reconsider your approach") {
			foundCorrection = true
			break
		}
	}
	if !foundCorrection {
		t.Error("self-correction message not found in tape")
	}
}

func TestIntegration_ParallelToolCalls(t *testing.T) {
	h := newTestHarness(t, []mockResponse{
		// LLM requests two tool calls at once.
		{toolCalls: []mockToolCall{
			{id: "tc1", name: "write_file", args: `{"path":"a.txt","content":"alpha"}`},
			{id: "tc2", name: "write_file", args: `{"path":"b.txt","content":"beta"}`},
		}},
		// Read both files back.
		{toolCalls: []mockToolCall{
			{id: "tc3", name: "read_file", args: `{"path":"a.txt"}`},
			{id: "tc4", name: "read_file", args: `{"path":"b.txt"}`},
		}},
		// Final answer.
		{content: "Files created: a.txt=alpha, b.txt=beta"},
	})

	result, err := h.agent.HandleInput(context.Background(), msg("Create and read two files"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if !strings.Contains(result, "alpha") || !strings.Contains(result, "beta") {
		t.Errorf("result = %q", result)
	}

	// Verify tape has tool results in order for both batches.
	entries, _ := h.tape.Entries()
	var toolResults []string
	for _, e := range entries {
		pm := e.PayloadMap()
		if pm["role"] == "tool" {
			name, _ := pm["name"].(string)
			toolResults = append(toolResults, name)
		}
	}
	// Should have 4 tool results total, in order.
	if len(toolResults) != 4 {
		t.Errorf("tool results = %d, want 4", len(toolResults))
	}
}

func TestIntegration_ShellToolExecution(t *testing.T) {
	h := newTestHarness(t, []mockResponse{
		{toolCalls: []mockToolCall{
			{id: "tc1", name: "shell_exec", args: `{"command":"echo integration_test_ok"}`},
		}},
		{content: "Shell returned: integration_test_ok"},
	})

	result, err := h.agent.HandleInput(context.Background(), msg("Run echo"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if !strings.Contains(result, "integration_test_ok") {
		t.Errorf("result = %q", result)
	}
}
