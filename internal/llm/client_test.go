package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Compile-time interface compliance checks.
var (
	_ Client = (*openaiClient)(nil)
	_ Client = (*anthropicClient)(nil)
)

func TestParseModelProvider(t *testing.T) {
	tests := []struct {
		input        string
		wantProvider Provider
		wantModel    string
	}{
		{"openai:gpt-4o", ProviderOpenAI, "gpt-4o"},
		{"anthropic:claude-sonnet-4-20250514", ProviderAnthropic, "claude-sonnet-4-20250514"},
		{"gpt-4o", ProviderOpenAI, "gpt-4o"},
		{"openai:gpt-4o-mini", ProviderOpenAI, "gpt-4o-mini"},
	}

	for _, tt := range tests {
		provider, model := ParseModelProvider(tt.input)
		if provider != tt.wantProvider || model != tt.wantModel {
			t.Errorf("ParseModelProvider(%q) = (%q, %q), want (%q, %q)",
				tt.input, provider, model, tt.wantProvider, tt.wantModel)
		}
	}
}

func TestNewClientValid(t *testing.T) {
	for _, p := range []Provider{ProviderOpenAI, ProviderAnthropic} {
		c, err := NewClient(p, "test-key")
		if err != nil {
			t.Errorf("NewClient(%q) unexpected error: %v", p, err)
		}
		if c.Provider() != p {
			t.Errorf("NewClient(%q).Provider() = %q, want %q", p, c.Provider(), p)
		}
	}
}

func TestNewClientUnknownProvider(t *testing.T) {
	_, err := NewClient("unknown", "test-key")
	if err == nil {
		t.Fatal("NewClient(\"unknown\") expected error, got nil")
	}
}

func TestRegisterProvider(t *testing.T) {
	// Register a custom provider.
	RegisterProvider("custom", "https://custom.example.com", func(apiKey, baseURL string) Client {
		return newOpenAIClient(apiKey, baseURL) // reuse OpenAI client for custom OpenAI-compatible endpoints
	})

	c, err := NewClient("custom", "test-key")
	if err != nil {
		t.Fatalf("NewClient(\"custom\") error: %v", err)
	}
	if c == nil {
		t.Fatal("NewClient(\"custom\") returned nil")
	}
}

func TestRegisterProvider_OverrideBaseURL(t *testing.T) {
	RegisterProvider("custom2", "https://default.example.com", func(apiKey, baseURL string) Client {
		return newOpenAIClient(apiKey, baseURL)
	})

	c, err := NewClient("custom2", "test-key", WithBaseURL("https://override.example.com"))
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}
	// Verify the override took effect by checking the client's baseURL.
	oc := c.(*openaiClient)
	if oc.baseURL != "https://override.example.com" {
		t.Errorf("baseURL = %q, want override URL", oc.baseURL)
	}
}

func TestAPIErrorFormat(t *testing.T) {
	err := &APIError{
		StatusCode: 429,
		Message:    "rate limited",
		Provider:   ProviderOpenAI,
		Retryable:  true,
	}
	want := "openai API error (HTTP 429): rate limited"
	if got := err.Error(); got != want {
		t.Errorf("APIError.Error() = %q, want %q", got, want)
	}
}

// ─── SSE Parser Tests ────────────────────────────────────────────

func TestSSEReader_BasicParsing(t *testing.T) {
	input := ": this is a comment\ndata: hello\n\ndata: world\n\n"
	reader := newSSEReader(strings.NewReader(input))

	ev, err := reader.Next()
	if err != nil {
		t.Fatalf("first event error: %v", err)
	}
	if ev.Data != "hello" {
		t.Errorf("first event data = %q, want %q", ev.Data, "hello")
	}

	ev, err = reader.Next()
	if err != nil {
		t.Fatalf("second event error: %v", err)
	}
	if ev.Data != "world" {
		t.Errorf("second event data = %q, want %q", ev.Data, "world")
	}

	_, err = reader.Next()
	if err != io.EOF {
		t.Errorf("expected io.EOF, got %v", err)
	}
}

func TestSSEReader_MultiLineData(t *testing.T) {
	input := "data: line1\ndata: line2\n\n"
	reader := newSSEReader(strings.NewReader(input))

	ev, err := reader.Next()
	if err != nil {
		t.Fatalf("event error: %v", err)
	}
	if ev.Data != "line1\nline2" {
		t.Errorf("data = %q, want %q", ev.Data, "line1\nline2")
	}
}

func TestSSEReader_WithEventTypes(t *testing.T) {
	input := "event: message_start\ndata: {\"type\":\"message_start\"}\n\nevent: ping\ndata: {}\n\n"
	reader := newSSEReader(strings.NewReader(input))

	ev, err := reader.Next()
	if err != nil {
		t.Fatalf("first event error: %v", err)
	}
	if ev.Event != "message_start" {
		t.Errorf("event type = %q, want %q", ev.Event, "message_start")
	}

	ev, err = reader.Next()
	if err != nil {
		t.Fatalf("second event error: %v", err)
	}
	if ev.Event != "ping" {
		t.Errorf("event type = %q, want %q", ev.Event, "ping")
	}
}

// ─── Retry Tests ─────────────────────────────────────────────────

func TestRetry_429ThenSuccess(t *testing.T) {
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if count.Add(1) == 1 {
			w.WriteHeader(429)
			return
		}
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	cfg := retryConfig{maxAttempts: 3, baseBackoff: 1 * time.Millisecond}
	resp, err := doWithRetry(context.Background(), cfg, func() (*http.Response, error) {
		return http.Get(srv.URL)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := count.Load(); got != 2 {
		t.Errorf("request count = %d, want 2", got)
	}
}

func TestRetry_NonRetryable400(t *testing.T) {
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.WriteHeader(400)
		w.Write([]byte("bad request"))
	}))
	defer srv.Close()

	cfg := retryConfig{maxAttempts: 3, baseBackoff: 1 * time.Millisecond}
	resp, err := doWithRetry(context.Background(), cfg, func() (*http.Response, error) {
		return http.Get(srv.URL)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if got := count.Load(); got != 1 {
		t.Errorf("request count = %d, want 1", got)
	}
}

func TestRetry_ExhaustedRetries(t *testing.T) {
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.WriteHeader(500)
	}))
	defer srv.Close()

	cfg := retryConfig{maxAttempts: 3, baseBackoff: 1 * time.Millisecond}
	_, err := doWithRetry(context.Background(), cfg, func() (*http.Response, error) {
		return http.Get(srv.URL)
	})
	if err == nil {
		t.Fatal("expected error after exhausted retries")
	}
	if got := count.Load(); got != 3 {
		t.Errorf("request count = %d, want 3", got)
	}
}

// ─── OpenAI Tests ────────────────────────────────────────────────

func TestOpenAIChat_TextResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify headers.
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer test-key")
		}

		resp := openaiChatResponse{
			Choices: []openaiChoice{{
				Message:      openaiMessage{Role: "assistant", Content: "Hello!"},
				FinishReason: "stop",
			}},
			Usage: openaiUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := newOpenAIClient("test-key", srv.URL)
	resp, err := client.Chat(context.Background(), ChatRequest{
		Model:    "gpt-4o",
		Messages: []Message{{Role: RoleUser, Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}

	if resp.Content != "Hello!" {
		t.Errorf("Content = %q, want %q", resp.Content, "Hello!")
	}
	if resp.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want %q", resp.FinishReason, "stop")
	}
	if resp.Usage.PromptTokens != 10 {
		t.Errorf("PromptTokens = %d, want 10", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 5 {
		t.Errorf("CompletionTokens = %d, want 5", resp.Usage.CompletionTokens)
	}
}

func TestOpenAIChat_ToolCallResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := openaiChatResponse{
			Choices: []openaiChoice{{
				Message: openaiMessage{
					Role: "assistant",
					ToolCalls: []openaiToolCall{{
						ID:   "call_123",
						Type: "function",
						Function: openaiToolFunction{
							Name:      "get_weather",
							Arguments: `{"city":"London"}`,
						},
					}},
				},
				FinishReason: "tool_calls",
			}},
			Usage: openaiUsage{PromptTokens: 20, CompletionTokens: 10, TotalTokens: 30},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := newOpenAIClient("test-key", srv.URL)
	resp, err := client.Chat(context.Background(), ChatRequest{
		Model:    "gpt-4o",
		Messages: []Message{{Role: RoleUser, Content: "What's the weather?"}},
		Tools: []ToolDefinition{{
			Name:        "get_weather",
			Description: "Get weather",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
		}},
	})
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}

	if resp.FinishReason != "tool_calls" {
		t.Errorf("FinishReason = %q, want %q", resp.FinishReason, "tool_calls")
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_123" || tc.Name != "get_weather" || tc.Arguments != `{"city":"London"}` {
		t.Errorf("ToolCall = %+v", tc)
	}
}

func TestOpenAIChatStream_Text(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		chunks := []string{
			`{"choices":[{"delta":{"content":"Hello"},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"content":" world"},"finish_reason":null}]}`,
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
			flusher.Flush()
		}
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	client := newOpenAIClient("test-key", srv.URL)
	ch, err := client.ChatStream(context.Background(), ChatRequest{
		Model:    "gpt-4o",
		Messages: []Message{{Role: RoleUser, Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	var content string
	var gotDone bool
	for ev := range ch {
		switch ev.Type {
		case StreamContentDelta:
			content += ev.Content
		case StreamDone:
			gotDone = true
		case StreamError:
			t.Fatalf("unexpected error event: %v", ev.Error)
		}
	}

	if content != "Hello world" {
		t.Errorf("content = %q, want %q", content, "Hello world")
	}
	if !gotDone {
		t.Error("never received StreamDone")
	}
}

func TestOpenAIChatStream_ToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		chunks := []string{
			// Initial chunk with tool call ID and name.
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"search","arguments":""}}]},"finish_reason":null}]}`,
			// Argument fragments.
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":"}}]},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"test\"}"}}]},"finish_reason":null}]}`,
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
			flusher.Flush()
		}
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	client := newOpenAIClient("test-key", srv.URL)
	ch, err := client.ChatStream(context.Background(), ChatRequest{
		Model:    "gpt-4o",
		Messages: []Message{{Role: RoleUser, Content: "search"}},
	})
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	var toolDeltas []ToolCallDelta
	for ev := range ch {
		if ev.Type == StreamToolCallDelta {
			toolDeltas = append(toolDeltas, *ev.ToolCall)
		}
		if ev.Type == StreamError {
			t.Fatalf("unexpected error: %v", ev.Error)
		}
	}

	if len(toolDeltas) != 3 {
		t.Fatalf("len(toolDeltas) = %d, want 3", len(toolDeltas))
	}
	if toolDeltas[0].ID != "call_abc" || toolDeltas[0].Name != "search" {
		t.Errorf("first delta = %+v", toolDeltas[0])
	}
	args := toolDeltas[1].Arguments + toolDeltas[2].Arguments
	if args != `{"q":"test"}` {
		t.Errorf("accumulated args = %q, want %q", args, `{"q":"test"}`)
	}
}

// ─── Anthropic Tests ─────────────────────────────────────────────

func TestAnthropicChat_TextResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify Anthropic-specific headers.
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Errorf("x-api-key = %q, want %q", got, "test-key")
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Errorf("anthropic-version = %q, want %q", got, "2023-06-01")
		}

		resp := anthropicResponse{
			Content:    []anthropicContentBlock{{Type: "text", Text: "Hi there!"}},
			StopReason: "end_turn",
			Usage:      anthropicUsage{InputTokens: 8, OutputTokens: 4},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := newAnthropicClient("test-key", srv.URL)
	resp, err := client.Chat(context.Background(), ChatRequest{
		Model:    "claude-sonnet-4-20250514",
		Messages: []Message{{Role: RoleUser, Content: "Hello"}},
	})
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}

	if resp.Content != "Hi there!" {
		t.Errorf("Content = %q, want %q", resp.Content, "Hi there!")
	}
	if resp.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want %q", resp.FinishReason, "stop")
	}
	if resp.Usage.PromptTokens != 8 {
		t.Errorf("PromptTokens = %d, want 8", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 4 {
		t.Errorf("CompletionTokens = %d, want 4", resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != 12 {
		t.Errorf("TotalTokens = %d, want 12", resp.Usage.TotalTokens)
	}
}

func TestAnthropicChat_ToolCallResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := anthropicResponse{
			Content: []anthropicContentBlock{{
				Type:  "tool_use",
				ID:    "toolu_123",
				Name:  "get_weather",
				Input: json.RawMessage(`{"city":"Paris"}`),
			}},
			StopReason: "tool_use",
			Usage:      anthropicUsage{InputTokens: 15, OutputTokens: 8},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := newAnthropicClient("test-key", srv.URL)
	resp, err := client.Chat(context.Background(), ChatRequest{
		Model:    "claude-sonnet-4-20250514",
		Messages: []Message{{Role: RoleUser, Content: "Weather in Paris?"}},
	})
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}

	if resp.FinishReason != "tool_calls" {
		t.Errorf("FinishReason = %q, want %q", resp.FinishReason, "tool_calls")
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "toolu_123" || tc.Name != "get_weather" || tc.Arguments != `{"city":"Paris"}` {
		t.Errorf("ToolCall = %+v", tc)
	}
}

func TestAnthropicChatStream_Text(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		events := []struct{ event, data string }{
			{"message_start", `{"type":"message_start"}`},
			{"content_block_start", `{"index":0,"content_block":{"type":"text","text":""}}`},
			{"content_block_delta", `{"index":0,"delta":{"type":"text_delta","text":"Hello"}}`},
			{"content_block_delta", `{"index":0,"delta":{"type":"text_delta","text":" world"}}`},
			{"content_block_stop", `{"index":0}`},
			{"message_stop", `{}`},
		}
		for _, e := range events {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.event, e.data)
			flusher.Flush()
		}
	}))
	defer srv.Close()

	client := newAnthropicClient("test-key", srv.URL)
	ch, err := client.ChatStream(context.Background(), ChatRequest{
		Model:    "claude-sonnet-4-20250514",
		Messages: []Message{{Role: RoleUser, Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	var content string
	var gotDone bool
	for ev := range ch {
		switch ev.Type {
		case StreamContentDelta:
			content += ev.Content
		case StreamDone:
			gotDone = true
		case StreamError:
			t.Fatalf("unexpected error: %v", ev.Error)
		}
	}

	if content != "Hello world" {
		t.Errorf("content = %q, want %q", content, "Hello world")
	}
	if !gotDone {
		t.Error("never received StreamDone")
	}
}

func TestAnthropicChatStream_ToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		events := []struct{ event, data string }{
			{"message_start", `{"type":"message_start"}`},
			{"content_block_start", `{"index":0,"content_block":{"type":"tool_use","id":"toolu_abc","name":"search"}}`},
			{"content_block_delta", `{"index":0,"delta":{"type":"input_json_delta","partial_json":"{\"q\":"}}`},
			{"content_block_delta", `{"index":0,"delta":{"type":"input_json_delta","partial_json":"\"test\"}"}}`},
			{"content_block_stop", `{"index":0}`},
			{"message_stop", `{}`},
		}
		for _, e := range events {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.event, e.data)
			flusher.Flush()
		}
	}))
	defer srv.Close()

	client := newAnthropicClient("test-key", srv.URL)
	ch, err := client.ChatStream(context.Background(), ChatRequest{
		Model:    "claude-sonnet-4-20250514",
		Messages: []Message{{Role: RoleUser, Content: "search"}},
	})
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	var toolDeltas []ToolCallDelta
	for ev := range ch {
		if ev.Type == StreamToolCallDelta {
			toolDeltas = append(toolDeltas, *ev.ToolCall)
		}
		if ev.Type == StreamError {
			t.Fatalf("unexpected error: %v", ev.Error)
		}
	}

	if len(toolDeltas) != 3 {
		t.Fatalf("len(toolDeltas) = %d, want 3", len(toolDeltas))
	}
	if toolDeltas[0].ID != "toolu_abc" || toolDeltas[0].Name != "search" {
		t.Errorf("first delta = %+v", toolDeltas[0])
	}
	args := toolDeltas[1].Arguments + toolDeltas[2].Arguments
	if args != `{"q":"test"}` {
		t.Errorf("accumulated args = %q, want %q", args, `{"q":"test"}`)
	}
}

// ─── Cross-Cutting Tests ─────────────────────────────────────────

func TestChat_APIError(t *testing.T) {
	tests := []struct {
		name      string
		provider  Provider
		newClient func(url string) Client
	}{
		{
			name:     "OpenAI",
			provider: ProviderOpenAI,
			newClient: func(url string) Client {
				return newOpenAIClient("bad-key", url)
			},
		},
		{
			name:     "Anthropic",
			provider: ProviderAnthropic,
			newClient: func(url string) Client {
				return newAnthropicClient("bad-key", url)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(401)
				w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
			}))
			defer srv.Close()

			client := tt.newClient(srv.URL)
			_, err := client.Chat(context.Background(), ChatRequest{
				Model:    "test-model",
				Messages: []Message{{Role: RoleUser, Content: "Hi"}},
			})
			if err == nil {
				t.Fatal("expected error, got nil")
			}

			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("expected *APIError, got %T: %v", err, err)
			}
			if apiErr.StatusCode != 401 {
				t.Errorf("StatusCode = %d, want 401", apiErr.StatusCode)
			}
			if apiErr.Provider != tt.provider {
				t.Errorf("Provider = %q, want %q", apiErr.Provider, tt.provider)
			}
			if apiErr.Retryable {
				t.Error("expected Retryable=false for 401")
			}
		})
	}
}

func TestAnthropicConversion_ToolResultMerging(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "What's the weather?"},
		{Role: RoleAssistant, Content: "", ToolCalls: []ToolCall{
			{ID: "t1", Name: "weather", Arguments: `{"city":"NYC"}`},
			{ID: "t2", Name: "weather", Arguments: `{"city":"LA"}`},
		}},
		{Role: RoleTool, ToolCallID: "t1", Content: "72F"},
		{Role: RoleTool, ToolCallID: "t2", Content: "85F"},
	}

	result := convertMessages(msgs)

	// Expected: user, assistant, user (with 2 tool_results merged)
	if len(result) != 3 {
		t.Fatalf("len(result) = %d, want 3", len(result))
	}

	// Last message should be user with 2 tool_result blocks.
	last := result[2]
	if last.Role != "user" {
		t.Errorf("last role = %q, want %q", last.Role, "user")
	}
	blocks, ok := last.Content.([]anthropicContentBlock)
	if !ok {
		t.Fatalf("last content type = %T, want []anthropicContentBlock", last.Content)
	}
	if len(blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want 2", len(blocks))
	}
	if blocks[0].ToolUseID != "t1" || blocks[1].ToolUseID != "t2" {
		t.Errorf("tool_use_ids = [%q, %q], want [t1, t2]", blocks[0].ToolUseID, blocks[1].ToolUseID)
	}
}

func TestAnthropicConversion_SystemFiltered(t *testing.T) {
	msgs := []Message{
		{Role: RoleSystem, Content: "You are helpful"},
		{Role: RoleUser, Content: "Hi"},
	}
	result := convertMessages(msgs)
	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1 (system filtered)", len(result))
	}
	if result[0].Role != "user" {
		t.Errorf("role = %q, want %q", result[0].Role, "user")
	}
}

func TestAnthropicConversion_ToolResultUsesContentField(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "Do something"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "t1", Name: "test", Arguments: `{}`}}},
		{Role: RoleTool, ToolCallID: "t1", Content: "tool output here"},
	}

	result := convertMessages(msgs)

	// The tool_result block should serialize with "content", not "text".
	last := result[len(result)-1]
	blocks, ok := last.Content.([]anthropicContentBlock)
	if !ok {
		t.Fatalf("expected []anthropicContentBlock, got %T", last.Content)
	}
	block := blocks[0]
	if block.Type != "tool_result" {
		t.Fatalf("type = %q, want %q", block.Type, "tool_result")
	}

	// Marshal to JSON and verify the field name is "content", not "text".
	data, err := json.Marshal(block)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	jsonStr := string(data)
	if !strings.Contains(jsonStr, `"content":"tool output here"`) {
		t.Errorf("tool_result JSON should use 'content' field, got: %s", jsonStr)
	}
	if strings.Contains(jsonStr, `"text":"tool output here"`) {
		t.Errorf("tool_result JSON should NOT use 'text' field for tool output, got: %s", jsonStr)
	}
}

func TestOpenAIChat_SystemPrompt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req openaiChatRequest
		json.NewDecoder(r.Body).Decode(&req)

		// Verify system message is prepended.
		if len(req.Messages) < 2 {
			t.Errorf("expected at least 2 messages, got %d", len(req.Messages))
		} else if req.Messages[0].Role != "system" || req.Messages[0].Content != "Be helpful" {
			t.Errorf("first message = %+v, want system 'Be helpful'", req.Messages[0])
		}

		resp := openaiChatResponse{
			Choices: []openaiChoice{{
				Message:      openaiMessage{Role: "assistant", Content: "OK"},
				FinishReason: "stop",
			}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := newOpenAIClient("test-key", srv.URL)
	_, err := client.Chat(context.Background(), ChatRequest{
		Model:        "gpt-4o",
		SystemPrompt: "Be helpful",
		Messages:     []Message{{Role: RoleUser, Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
}

func TestAnthropicChat_SystemPrompt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req anthropicRequest
		json.NewDecoder(r.Body).Decode(&req)

		// Verify system is a top-level field, not a message.
		if req.System != "Be helpful" {
			t.Errorf("system = %q, want %q", req.System, "Be helpful")
		}

		resp := anthropicResponse{
			Content:    []anthropicContentBlock{{Type: "text", Text: "OK"}},
			StopReason: "end_turn",
			Usage:      anthropicUsage{InputTokens: 5, OutputTokens: 2},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := newAnthropicClient("test-key", srv.URL)
	_, err := client.Chat(context.Background(), ChatRequest{
		Model:        "claude-sonnet-4-20250514",
		SystemPrompt: "Be helpful",
		Messages:     []Message{{Role: RoleUser, Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
}

// ─── Streaming Context Cancellation Tests ────────────────────────

func TestOpenAIChatStream_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		// Send one chunk immediately, then stall with 1s delays.
		chunks := []string{
			`{"choices":[{"delta":{"content":"Hello"},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"content":" slow"},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"content":" world"},"finish_reason":null}]}`,
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
			flusher.Flush()
			time.Sleep(1 * time.Second)
		}
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	client := newOpenAIClient("test-key", srv.URL)
	ch, err := client.ChatStream(ctx, ChatRequest{
		Model:    "gpt-4o",
		Messages: []Message{{Role: RoleUser, Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	var gotError bool
	for ev := range ch {
		if ev.Type == StreamError {
			gotError = true
			if !errors.Is(ev.Error, context.DeadlineExceeded) {
				// The error may be wrapped; check that it relates to context cancellation.
				if !strings.Contains(ev.Error.Error(), "context deadline exceeded") &&
					!strings.Contains(ev.Error.Error(), "context canceled") {
					t.Errorf("expected context deadline error, got: %v", ev.Error)
				}
			}
		}
		if ev.Type == StreamDone {
			t.Error("should not receive StreamDone when context is cancelled")
		}
	}

	// Either we got an error event, or the channel closed early due to context cancellation.
	// Both are acceptable behaviors — the key is no panic or hang.
	_ = gotError
}

func TestAnthropicChatStream_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		events := []struct{ event, data string }{
			{"message_start", `{"type":"message_start"}`},
			{"content_block_start", `{"index":0,"content_block":{"type":"text","text":""}}`},
			{"content_block_delta", `{"index":0,"delta":{"type":"text_delta","text":"Hello"}}`},
		}

		for _, e := range events {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.event, e.data)
			flusher.Flush()
			time.Sleep(1 * time.Second) // Slow delivery
		}

		// These should never be reached due to context timeout.
		fmt.Fprintf(w, "event: content_block_stop\ndata: {\"index\":0}\n\n")
		fmt.Fprintf(w, "event: message_stop\ndata: {}\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	client := newAnthropicClient("test-key", srv.URL)
	ch, err := client.ChatStream(ctx, ChatRequest{
		Model:    "claude-sonnet-4-20250514",
		Messages: []Message{{Role: RoleUser, Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	var gotError bool
	for ev := range ch {
		if ev.Type == StreamError {
			gotError = true
			if !errors.Is(ev.Error, context.DeadlineExceeded) {
				if !strings.Contains(ev.Error.Error(), "context deadline exceeded") &&
					!strings.Contains(ev.Error.Error(), "context canceled") {
					t.Errorf("expected context deadline error, got: %v", ev.Error)
				}
			}
		}
		if ev.Type == StreamDone {
			t.Error("should not receive StreamDone when context is cancelled")
		}
	}

	_ = gotError
}

// ─── Streaming Malformed SSE Tests ───────────────────────────────

func TestOpenAIChatStream_MalformedSSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		// Send valid chunk, then malformed JSON, then close.
		fmt.Fprintf(w, "data: %s\n\n",
			`{"choices":[{"delta":{"content":"Hello"},"finish_reason":null}]}`)
		flusher.Flush()

		// Malformed JSON in data field.
		fmt.Fprintf(w, "data: {not valid json!!! missing quotes\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	client := newOpenAIClient("test-key", srv.URL)
	ch, err := client.ChatStream(context.Background(), ChatRequest{
		Model:    "gpt-4o",
		Messages: []Message{{Role: RoleUser, Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	var gotContent bool
	var gotError bool
	for ev := range ch {
		switch ev.Type {
		case StreamContentDelta:
			gotContent = true
		case StreamError:
			gotError = true
		}
	}

	// Should have received the valid content before the error.
	if !gotContent {
		t.Error("expected at least one content delta before the malformed data")
	}
	// Should have received an error for the malformed JSON.
	if !gotError {
		t.Error("expected StreamError for malformed JSON data")
	}
}

func TestAnthropicChatStream_MalformedSSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		// Send valid events, then malformed JSON, then close.
		fmt.Fprintf(w, "event: message_start\ndata: %s\n\n", `{"type":"message_start"}`)
		flusher.Flush()

		fmt.Fprintf(w, "event: content_block_start\ndata: %s\n\n",
			`{"index":0,"content_block":{"type":"text","text":""}}`)
		flusher.Flush()

		fmt.Fprintf(w, "event: content_block_delta\ndata: %s\n\n",
			`{"index":0,"delta":{"type":"text_delta","text":"Hello"}}`)
		flusher.Flush()

		// Malformed JSON in content_block_delta data field.
		fmt.Fprintf(w, "event: content_block_delta\ndata: {broken json\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	client := newAnthropicClient("test-key", srv.URL)
	ch, err := client.ChatStream(context.Background(), ChatRequest{
		Model:    "claude-sonnet-4-20250514",
		Messages: []Message{{Role: RoleUser, Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	var gotContent bool
	var gotError bool
	for ev := range ch {
		switch ev.Type {
		case StreamContentDelta:
			gotContent = true
		case StreamError:
			gotError = true
		}
	}

	if !gotContent {
		t.Error("expected at least one content delta before the malformed data")
	}
	if !gotError {
		t.Error("expected StreamError for malformed JSON data")
	}
}

// ─── Multimodal (Image) Tests ────────────────────────────────────

func TestOpenAI_BuildRequest_WithImages(t *testing.T) {
	client := newOpenAIClient("test-key", "https://api.example.com")
	req := ChatRequest{
		Model: "gpt-4o",
		Messages: []Message{{
			Role:    RoleUser,
			Content: "What's in this image?",
			Images: []ImageContent{{
				Base64:    "aGVsbG8=",
				MediaType: "image/png",
			}},
		}},
	}

	wireReq := client.buildRequest(req, false)

	if len(wireReq.Messages) != 1 {
		t.Fatalf("len(Messages) = %d, want 1", len(wireReq.Messages))
	}

	msg := wireReq.Messages[0]
	parts, ok := msg.Content.([]openaiContentPart)
	if !ok {
		t.Fatalf("Content type = %T, want []openaiContentPart", msg.Content)
	}
	if len(parts) != 2 {
		t.Fatalf("len(parts) = %d, want 2", len(parts))
	}
	if parts[0].Type != "text" || parts[0].Text != "What's in this image?" {
		t.Errorf("parts[0] = %+v, want text part", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL == nil {
		t.Fatalf("parts[1] = %+v, want image_url part", parts[1])
	}
	wantURL := "data:image/png;base64,aGVsbG8="
	if parts[1].ImageURL.URL != wantURL {
		t.Errorf("image URL = %q, want %q", parts[1].ImageURL.URL, wantURL)
	}
}

func TestOpenAI_BuildRequest_NoImages(t *testing.T) {
	client := newOpenAIClient("test-key", "https://api.example.com")
	req := ChatRequest{
		Model:    "gpt-4o",
		Messages: []Message{{Role: RoleUser, Content: "Hello"}},
	}

	wireReq := client.buildRequest(req, false)

	msg := wireReq.Messages[0]
	s, ok := msg.Content.(string)
	if !ok {
		t.Fatalf("Content type = %T, want string", msg.Content)
	}
	if s != "Hello" {
		t.Errorf("Content = %q, want %q", s, "Hello")
	}
}

func TestAnthropic_ConvertMessages_WithImages(t *testing.T) {
	msgs := []Message{{
		Role:    RoleUser,
		Content: "Describe this image",
		Images: []ImageContent{{
			Base64:    "aW1hZ2U=",
			MediaType: "image/jpeg",
		}},
	}}

	result := convertMessages(msgs)

	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}

	blocks, ok := result[0].Content.([]anthropicContentBlock)
	if !ok {
		t.Fatalf("Content type = %T, want []anthropicContentBlock", result[0].Content)
	}
	if len(blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want 2", len(blocks))
	}
	if blocks[0].Type != "text" || blocks[0].Text != "Describe this image" {
		t.Errorf("blocks[0] = %+v, want text block", blocks[0])
	}
	if blocks[1].Type != "image" || blocks[1].Source == nil {
		t.Fatalf("blocks[1] = %+v, want image block", blocks[1])
	}
	if blocks[1].Source.Type != "base64" {
		t.Errorf("source type = %q, want %q", blocks[1].Source.Type, "base64")
	}
	if blocks[1].Source.MediaType != "image/jpeg" {
		t.Errorf("media_type = %q, want %q", blocks[1].Source.MediaType, "image/jpeg")
	}
	if blocks[1].Source.Data != "aW1hZ2U=" {
		t.Errorf("data = %q, want %q", blocks[1].Source.Data, "aW1hZ2U=")
	}
}

func TestAnthropic_ConvertMessages_NoImages(t *testing.T) {
	msgs := []Message{{
		Role:    RoleUser,
		Content: "Hello",
	}}

	result := convertMessages(msgs)

	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}

	s, ok := result[0].Content.(string)
	if !ok {
		t.Fatalf("Content type = %T, want string", result[0].Content)
	}
	if s != "Hello" {
		t.Errorf("Content = %q, want %q", s, "Hello")
	}
}
