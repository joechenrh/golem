package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Wire-format structs for Anthropic Messages API.

type anthropicRequest struct {
	Model       string             `json:"model"`
	Messages    []anthropicMessage `json:"messages"`
	System      string             `json:"system,omitempty"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature *float64           `json:"temperature,omitempty"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string or []anthropicContentBlock
}

type anthropicContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`          // tool_use
	Name      string          `json:"name,omitempty"`        // tool_use
	Input     json.RawMessage `json:"input,omitempty"`       // tool_use (JSON object)
	ToolUseID string          `json:"tool_use_id,omitempty"` // tool_result
	Content   string          `json:"content,omitempty"`     // tool_result (reuses Text for simple string)
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// Response structs.

type anthropicResponse struct {
	Content    []anthropicContentBlock `json:"content"`
	StopReason string                  `json:"stop_reason"`
	Usage      anthropicUsage          `json:"usage"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Streaming structs.

type anthropicStreamContentBlockStart struct {
	Index        int                   `json:"index"`
	ContentBlock anthropicContentBlock `json:"content_block"`
}

type anthropicStreamContentBlockDelta struct {
	Index int                         `json:"index"`
	Delta anthropicStreamDeltaPayload `json:"delta"`
}

type anthropicStreamDeltaPayload struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`         // text_delta
	PartialJSON string `json:"partial_json,omitempty"` // input_json_delta
}

type anthropicStreamMessageDelta struct {
	Delta struct {
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage *anthropicUsage `json:"usage,omitempty"`
}

// Error response.

type anthropicErrorResponse struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Client implementation.

type anthropicClient struct {
	apiKey     string
	baseURL    string
	http       *http.Client
	streamHTTP *http.Client
}

func newAnthropicClient(apiKey, baseURL string) *anthropicClient {
	return &anthropicClient{
		apiKey:     apiKey,
		baseURL:    baseURL,
		http:       &http.Client{Timeout: 120 * time.Second},
		streamHTTP: &http.Client{Timeout: 0},
	}
}

func (c *anthropicClient) Provider() Provider {
	return ProviderAnthropic
}

func (c *anthropicClient) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	wireReq := c.buildRequest(req, false)

	body, err := json.Marshal(wireReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshal request: %w", err)
	}

	resp, err := doWithRetry(ctx, defaultRetryConfig(), func() (*http.Response, error) {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/messages", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		c.setHeaders(httpReq)
		return c.http.Do(httpReq)
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.parseError(resp)
	}

	var wireResp anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&wireResp); err != nil {
		return nil, fmt.Errorf("anthropic: decode response: %w", err)
	}

	return c.convertResponse(wireResp), nil
}

func (c *anthropicClient) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	wireReq := c.buildRequest(req, true)

	body, err := json.Marshal(wireReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.setHeaders(httpReq)

	resp, err := c.streamHTTP.Do(httpReq)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, c.parseError(resp)
	}

	ch := make(chan StreamEvent, streamBufferSize)
	go c.readStream(ctx, resp.Body, ch)
	return ch, nil
}

func (c *anthropicClient) readStream(ctx context.Context, body io.ReadCloser, ch chan<- StreamEvent) {
	defer close(ch)
	defer body.Close()

	reader := newSSEReader(body)
	for {
		ev, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			sendEvent(ctx, ch, StreamEvent{Type: StreamError, Error: err})
			return
		}

		switch ev.Event {
		case "content_block_start":
			var cbs anthropicStreamContentBlockStart
			if err := json.Unmarshal([]byte(ev.Data), &cbs); err != nil {
				sendEvent(ctx, ch, StreamEvent{Type: StreamError, Error: err})
				return
			}
			if cbs.ContentBlock.Type == "tool_use" {
				sendEvent(ctx, ch, StreamEvent{
					Type: StreamToolCallDelta,
					ToolCall: &ToolCallDelta{
						ID:   cbs.ContentBlock.ID,
						Name: cbs.ContentBlock.Name,
					},
				})
			}

		case "content_block_delta":
			var cbd anthropicStreamContentBlockDelta
			if err := json.Unmarshal([]byte(ev.Data), &cbd); err != nil {
				sendEvent(ctx, ch, StreamEvent{Type: StreamError, Error: err})
				return
			}
			switch cbd.Delta.Type {
			case "text_delta":
				sendEvent(ctx, ch, StreamEvent{
					Type:    StreamContentDelta,
					Content: cbd.Delta.Text,
				})
			case "input_json_delta":
				sendEvent(ctx, ch, StreamEvent{
					Type: StreamToolCallDelta,
					ToolCall: &ToolCallDelta{
						Arguments: cbd.Delta.PartialJSON,
					},
				})
			}

		case "message_stop":
			sendEvent(ctx, ch, StreamEvent{Type: StreamDone})
			return

		case "message_delta":
			// Contains stop_reason and usage; we don't need to emit anything special.

		case "ping", "content_block_stop", "message_start":
			// Ignored.
		}
	}
}

func (c *anthropicClient) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
}

func (c *anthropicClient) buildRequest(req ChatRequest, stream bool) anthropicRequest {
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}

	wireReq := anthropicRequest{
		Model:     req.Model,
		System:    req.SystemPrompt,
		MaxTokens: maxTokens,
		Stream:    stream,
	}
	if req.Temperature != 0 {
		wireReq.Temperature = &req.Temperature
	}

	wireReq.Messages = convertMessages(req.Messages)

	for _, td := range req.Tools {
		wireReq.Tools = append(wireReq.Tools, anthropicTool{
			Name:        td.Name,
			Description: td.Description,
			InputSchema: td.Parameters,
		})
	}

	return wireReq
}

// convertMessages transforms unified messages to Anthropic wire format.
// It filters system messages and merges consecutive tool messages into user messages
// with tool_result content blocks (Anthropic requires alternating user/assistant).
func convertMessages(msgs []Message) []anthropicMessage {
	var result []anthropicMessage

	for _, m := range msgs {
		if m.Role == RoleSystem {
			continue
		}

		switch m.Role {
		case RoleUser:
			result = append(result, anthropicMessage{
				Role:    "user",
				Content: m.Content,
			})

		case RoleAssistant:
			if len(m.ToolCalls) > 0 {
				var blocks []anthropicContentBlock
				if m.Content != "" {
					blocks = append(blocks, anthropicContentBlock{
						Type: "text",
						Text: m.Content,
					})
				}
				for _, tc := range m.ToolCalls {
					blocks = append(blocks, anthropicContentBlock{
						Type:  "tool_use",
						ID:    tc.ID,
						Name:  tc.Name,
						Input: json.RawMessage(tc.Arguments),
					})
				}
				result = append(result, anthropicMessage{
					Role:    "assistant",
					Content: blocks,
				})
			} else {
				result = append(result, anthropicMessage{
					Role:    "assistant",
					Content: m.Content,
				})
			}

		case RoleTool:
			block := anthropicContentBlock{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   m.Content,
			}
			// Merge into previous user message if it has tool_result blocks.
			if len(result) > 0 {
				prev := &result[len(result)-1]
				if prev.Role == "user" {
					if blocks, ok := prev.Content.([]anthropicContentBlock); ok {
						prev.Content = append(blocks, block)
						continue
					}
				}
			}
			result = append(result, anthropicMessage{
				Role:    "user",
				Content: []anthropicContentBlock{block},
			})
		}
	}

	return result
}

func (c *anthropicClient) convertResponse(resp anthropicResponse) *ChatResponse {
	cr := &ChatResponse{
		Usage: Usage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
	}

	// Map stop_reason.
	switch resp.StopReason {
	case "end_turn":
		cr.FinishReason = "stop"
	case "tool_use":
		cr.FinishReason = "tool_calls"
	case "max_tokens":
		cr.FinishReason = "length"
	default:
		cr.FinishReason = resp.StopReason
	}

	// Extract content and tool calls from content blocks.
	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			if cr.Content != "" {
				cr.Content += block.Text
			} else {
				cr.Content = block.Text
			}
		case "tool_use":
			cr.ToolCalls = append(cr.ToolCalls, ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: string(block.Input),
			})
		}
	}

	return cr
}

func (c *anthropicClient) parseError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	msg := string(body)

	var errResp anthropicErrorResponse
	if json.Unmarshal(body, &errResp) == nil && errResp.Error.Message != "" {
		msg = errResp.Error.Message
	}

	return &APIError{
		StatusCode: resp.StatusCode,
		Message:    msg,
		Provider:   ProviderAnthropic,
		Retryable:  resp.StatusCode == 429 || resp.StatusCode >= 500,
	}
}
