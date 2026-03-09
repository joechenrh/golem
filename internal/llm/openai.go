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

	"go.uber.org/zap"
)

// Wire-format structs for OpenAI Chat Completions API.

type openaiChatRequest struct {
	Model           string          `json:"model"`
	Messages        []openaiMessage `json:"messages"`
	Tools           []openaiTool    `json:"tools,omitempty"`
	MaxTokens       int             `json:"max_tokens,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	Stream          bool            `json:"stream,omitempty"`
	ResponseFormat  *ResponseFormat `json:"response_format,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
	StreamOptions   *streamOptions  `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openaiMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content"`
	ToolCalls  []openaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
}

type openaiContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *openaiImageURL `json:"image_url,omitempty"`
}

type openaiImageURL struct {
	URL string `json:"url"`
}

type openaiToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openaiToolFunction `json:"function"`
}

type openaiToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openaiTool struct {
	Type     string               `json:"type"`
	Function openaiToolDefinition `json:"function"`
}

type openaiToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// Response structs.

type openaiChatResponse struct {
	Choices []openaiChoice `json:"choices"`
	Usage   openaiUsage    `json:"usage"`
}

type openaiChoice struct {
	Message      openaiMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

type openaiUsage struct {
	PromptTokens            int                            `json:"prompt_tokens"`
	CompletionTokens        int                            `json:"completion_tokens"`
	TotalTokens             int                            `json:"total_tokens"`
	CompletionTokensDetails *openaiCompletionTokensDetails `json:"completion_tokens_details,omitempty"`
}

type openaiCompletionTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

// Streaming structs.

type openaiStreamChunk struct {
	Choices []openaiStreamChoice `json:"choices"`
	Usage   *openaiUsage         `json:"usage,omitempty"`
}

type openaiStreamChoice struct {
	Delta        openaiStreamDelta `json:"delta"`
	FinishReason *string           `json:"finish_reason"`
}

type openaiStreamDelta struct {
	Content   string                 `json:"content,omitempty"`
	ToolCalls []openaiStreamToolCall `json:"tool_calls,omitempty"`
}

type openaiStreamToolCall struct {
	Index    int                `json:"index"`
	ID       string             `json:"id,omitempty"`
	Type     string             `json:"type,omitempty"`
	Function openaiToolFunction `json:"function"`
}

// Client implementation.

type openaiClient struct {
	apiKey        string
	baseURL       string
	http          *http.Client
	streamHTTP    *http.Client
	logger        *zap.Logger
	responsesMode bool // use Responses API instead of Chat Completions
}

// NewOpenAICompatibleClient creates an OpenAI-compatible client.
// Use this as the factory when registering custom providers that expose
// an OpenAI-compatible chat completions API.
func NewOpenAICompatibleClient(apiKey, baseURL string) Client {
	return newOpenAIClient(apiKey, baseURL)
}

func newOpenAIClient(apiKey, baseURL string) *openaiClient {
	return &openaiClient{
		apiKey:     apiKey,
		baseURL:    baseURL,
		http:       &http.Client{Timeout: 120 * time.Second},
		streamHTTP: &http.Client{Timeout: 0}, // no timeout; use context cancellation
		logger:     zap.NewNop(),
	}
}

func (c *openaiClient) setLogger(l *zap.Logger) { c.logger = l }
func (c *openaiClient) setResponsesMode(v bool) { c.responsesMode = v }

func (c *openaiClient) Provider() Provider {
	return ProviderOpenAI
}

func (c *openaiClient) Chat(
	ctx context.Context, req ChatRequest,
) (*ChatResponse, error) {
	if c.responsesMode {
		return c.chatResponses(ctx, req)
	}

	wireReq := c.buildRequest(req, false)

	body, err := json.Marshal(wireReq)
	if err != nil {
		return nil, fmt.Errorf("openai: marshal request: %w", err)
	}

	retryCfg := defaultRetryConfig()
	retryCfg.logger = c.logger
	resp, err := doWithRetry(ctx, retryCfg, func() (*http.Response, error) {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
		return c.http.Do(httpReq)
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.parseError(resp)
	}

	var wireResp openaiChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&wireResp); err != nil {
		return nil, fmt.Errorf("openai: decode response: %w", err)
	}

	return c.convertResponse(wireResp), nil
}

func (c *openaiClient) ChatStream(
	ctx context.Context, req ChatRequest,
) (<-chan StreamEvent, error) {
	if c.responsesMode {
		return c.chatResponsesStream(ctx, req)
	}

	wireReq := c.buildRequest(req, true)

	body, err := json.Marshal(wireReq)
	if err != nil {
		return nil, fmt.Errorf("openai: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

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

func (c *openaiClient) readStream(
	ctx context.Context, body io.ReadCloser,
	ch chan<- StreamEvent,
) {
	defer close(ch)
	defer body.Close()

	var usage *Usage
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

		if ev.Data == "[DONE]" {
			sendEvent(ctx, ch, StreamEvent{Type: StreamDone, Usage: usage})
			return
		}

		var chunk openaiStreamChunk
		if err := json.Unmarshal([]byte(ev.Data), &chunk); err != nil {
			sendEvent(ctx, ch, StreamEvent{Type: StreamError, Error: fmt.Errorf("openai: unmarshal chunk: %w", err)})
			return
		}

		// Capture usage from the final chunk (OpenAI sends it with stream_options).
		if chunk.Usage != nil {
			usage = &Usage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
				TotalTokens:      chunk.Usage.TotalTokens,
			}
			if chunk.Usage.CompletionTokensDetails != nil {
				usage.ReasoningTokens = chunk.Usage.CompletionTokensDetails.ReasoningTokens
			}
		}

		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				sendEvent(ctx, ch, StreamEvent{
					Type:    StreamContentDelta,
					Content: choice.Delta.Content,
				})
			}
			for _, tc := range choice.Delta.ToolCalls {
				sendEvent(ctx, ch, StreamEvent{
					Type: StreamToolCallDelta,
					ToolCall: &ToolCallDelta{
						ID:        tc.ID,
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				})
			}
		}
	}
}

func (c *openaiClient) buildRequest(
	req ChatRequest, stream bool,
) openaiChatRequest {
	var msgs []openaiMessage

	if req.SystemPrompt != "" {
		msgs = append(msgs, openaiMessage{
			Role:    "system",
			Content: req.SystemPrompt,
		})
	}

	for _, m := range req.Messages {
		om := openaiMessage{
			Role:       string(m.Role),
			ToolCallID: m.ToolCallID,
			Name:       m.Name,
		}

		// Build multipart content when images are present.
		if len(m.Images) > 0 && m.Role == RoleUser {
			var parts []openaiContentPart
			if m.Content != "" {
				parts = append(parts, openaiContentPart{Type: "text", Text: m.Content})
			}
			for _, img := range m.Images {
				parts = append(parts, openaiContentPart{
					Type: "image_url",
					ImageURL: &openaiImageURL{
						URL: "data:" + img.MediaType + ";base64," + img.Base64,
					},
				})
			}
			om.Content = parts
		} else {
			om.Content = m.Content
		}

		for _, tc := range m.ToolCalls {
			om.ToolCalls = append(om.ToolCalls, openaiToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: openaiToolFunction{
					Name:      tc.Name,
					Arguments: tc.Arguments,
				},
			})
		}
		msgs = append(msgs, om)
	}

	wireReq := openaiChatRequest{
		Model:           req.Model,
		Messages:        msgs,
		MaxTokens:       req.MaxTokens,
		Stream:          stream,
		ResponseFormat:  req.ResponseFormat,
		ReasoningEffort: req.ReasoningEffort,
	}
	if stream {
		wireReq.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	wireReq.Temperature = req.Temperature

	for _, td := range req.Tools {
		wireReq.Tools = append(wireReq.Tools, openaiTool{
			Type: "function",
			Function: openaiToolDefinition{
				Name:        td.Name,
				Description: td.Description,
				Parameters:  td.Parameters,
			},
		})
	}

	return wireReq
}

func (c *openaiClient) convertResponse(
	resp openaiChatResponse,
) *ChatResponse {
	cr := &ChatResponse{
		Usage: Usage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		},
	}
	if resp.Usage.CompletionTokensDetails != nil {
		cr.Usage.ReasoningTokens = resp.Usage.CompletionTokensDetails.ReasoningTokens
	}

	if len(resp.Choices) > 0 {
		choice := resp.Choices[0]
		if s, ok := choice.Message.Content.(string); ok {
			cr.Content = s
		}
		cr.FinishReason = choice.FinishReason
		for _, tc := range choice.Message.ToolCalls {
			cr.ToolCalls = append(cr.ToolCalls, ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: NormalizeArgs(tc.Function.Arguments),
			})
		}
	}

	return cr
}

func (c *openaiClient) parseError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	msg := string(body)

	// Try to extract error message from JSON response.
	var errResp struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Error.Message != "" {
		msg = errResp.Error.Message
	}

	return &APIError{
		StatusCode: resp.StatusCode,
		Message:    msg,
		Provider:   ProviderOpenAI,
		Retryable:  resp.StatusCode == 429 || resp.StatusCode >= 500,
	}
}

// sendEvent sends an event to the channel, respecting context cancellation.
func sendEvent(
	ctx context.Context, ch chan<- StreamEvent,
	ev StreamEvent,
) {
	select {
	case ch <- ev:
	case <-ctx.Done():
	}
}
