package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Wire-format structs for OpenAI Responses API.

type responsesRequest struct {
	Model              string               `json:"model"`
	Input              any                  `json:"input"`
	Instructions       string               `json:"instructions,omitempty"`
	Tools              []responsesTool      `json:"tools,omitempty"`
	PreviousResponseID string               `json:"previous_response_id,omitempty"`
	MaxOutputTokens    int                  `json:"max_output_tokens,omitempty"`
	Temperature        *float64             `json:"temperature,omitempty"`
	Stream             bool                 `json:"stream,omitempty"`
	Reasoning          *responsesReasoning  `json:"reasoning,omitempty"`
	Truncation         *responsesTruncation `json:"truncation,omitempty"`
	Store              *bool                `json:"store,omitempty"`
}

type responsesTruncation struct {
	Type string `json:"type"`
}

type responsesReasoning struct {
	Effort string `json:"effort"`
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// Input item types for the Responses API.

type responsesInputItem struct {
	Type      string `json:"type"`
	Role      string `json:"role,omitempty"`
	Content   any    `json:"content,omitempty"`   // string or []responsesContentPart
	CallID    string `json:"call_id,omitempty"`   // function_call_output
	Output    string `json:"output,omitempty"`    // function_call_output
	ID        string `json:"id,omitempty"`        // function_call (for assistant tool calls)
	Name      string `json:"name,omitempty"`      // function_call
	Arguments string `json:"arguments,omitempty"` // function_call
}

type responsesContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

// Response structs.

type responsesResponse struct {
	ID     string                `json:"id"`
	Output []responsesOutputItem `json:"output"`
	Usage  responsesUsage        `json:"usage"`
	Status string                `json:"status"`
}

type responsesOutputItem struct {
	Type      string `json:"type"`
	ID        string `json:"id,omitempty"`        // function_call ID
	CallID    string `json:"call_id,omitempty"`   // function_call
	Name      string `json:"name,omitempty"`      // function_call
	Arguments string `json:"arguments,omitempty"` // function_call
	Status    string `json:"status,omitempty"`    // web_search_call status
	Content   []struct {
		Type        string                `json:"type"`
		Text        string                `json:"text,omitempty"`
		Annotations []responsesAnnotation `json:"annotations,omitempty"`
	} `json:"content,omitempty"` // message content
}

type responsesAnnotation struct {
	Type  string `json:"type"`
	URL   string `json:"url,omitempty"`
	Title string `json:"title,omitempty"`
	Start int    `json:"start_index,omitempty"`
	End   int    `json:"end_index,omitempty"`
}

type responsesUsage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	TotalTokens         int `json:"total_tokens"`
	OutputTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details,omitempty"`
}

// buildResponsesRequest translates a ChatRequest into a Responses API request.
func (c *openaiClient) buildResponsesRequest(req ChatRequest, stream bool) responsesRequest {
	wireReq := responsesRequest{
		Model:              req.Model,
		Instructions:       req.SystemPrompt,
		MaxOutputTokens:    req.MaxTokens,
		PreviousResponseID: req.PreviousResponseID,
		Stream:             stream,
	}
	wireReq.Temperature = req.Temperature
	if req.ReasoningEffort != "" {
		wireReq.Reasoning = &responsesReasoning{Effort: req.ReasoningEffort}
	}

	// Build input: use IncrementalInput if chaining, else full Messages.
	msgs := req.Messages
	if req.PreviousResponseID != "" && len(req.IncrementalInput) > 0 {
		msgs = req.IncrementalInput
	}
	wireReq.Input = convertToResponsesInput(msgs)

	// Truncation.
	if req.Truncation == "auto" {
		wireReq.Truncation = &responsesTruncation{Type: "auto"}
	}

	// Store.
	if req.Store != nil {
		wireReq.Store = req.Store
	}

	// Tools.
	for _, td := range req.Tools {
		// When native web search is enabled, emit web_search_preview instead
		// of the function-based web_search tool definition.
		if req.UseNativeWebSearch && td.Name == "web_search" {
			wireReq.Tools = append(wireReq.Tools, responsesTool{
				Type: "web_search_preview",
			})
			continue
		}
		wireReq.Tools = append(wireReq.Tools, responsesTool{
			Type:        "function",
			Name:        td.Name,
			Description: td.Description,
			Parameters:  td.Parameters,
		})
	}

	return wireReq
}

// convertToResponsesInput converts unified Messages to Responses API input items.
func convertToResponsesInput(msgs []Message) []responsesInputItem {
	var items []responsesInputItem
	for _, m := range msgs {
		switch m.Role {
		case RoleUser:
			item := responsesInputItem{
				Type: "message",
				Role: "user",
			}
			if len(m.Images) > 0 {
				var parts []responsesContentPart
				if m.Content != "" {
					parts = append(parts, responsesContentPart{Type: "input_text", Text: m.Content})
				}
				for _, img := range m.Images {
					parts = append(parts, responsesContentPart{
						Type:     "input_image",
						ImageURL: "data:" + img.MediaType + ";base64," + img.Base64,
					})
				}
				item.Content = parts
			} else {
				item.Content = m.Content
			}
			items = append(items, item)

		case RoleAssistant:
			// Assistant text becomes a message.
			if m.Content != "" {
				items = append(items, responsesInputItem{
					Type:    "message",
					Role:    "assistant",
					Content: m.Content,
				})
			}
			// Assistant tool calls become function_call items.
			for _, tc := range m.ToolCalls {
				items = append(items, responsesInputItem{
					Type:      "function_call",
					ID:        tc.ID,
					CallID:    tc.ID,
					Name:      tc.Name,
					Arguments: tc.Arguments,
				})
			}

		case RoleTool:
			items = append(items, responsesInputItem{
				Type:   "function_call_output",
				CallID: m.ToolCallID,
				Output: m.Content,
			})

		case RoleSystem:
			// System messages are handled via Instructions field.
			continue
		}
	}
	return items
}

// chatResponses performs a non-streaming Responses API call.
func (c *openaiClient) chatResponses(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	wireReq := c.buildResponsesRequest(req, false)

	body, err := json.Marshal(wireReq)
	if err != nil {
		return nil, fmt.Errorf("openai responses: marshal: %w", err)
	}

	retryCfg := defaultRetryConfig()
	retryCfg.logger = c.logger
	resp, err := doWithRetry(ctx, retryCfg, func() (*http.Response, error) {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/responses", bytes.NewReader(body))
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

	var wireResp responsesResponse
	if err := json.NewDecoder(resp.Body).Decode(&wireResp); err != nil {
		return nil, fmt.Errorf("openai responses: decode: %w", err)
	}

	return c.convertResponsesResponse(wireResp), nil
}

// convertResponsesResponse converts a Responses API response to a unified ChatResponse.
func (c *openaiClient) convertResponsesResponse(resp responsesResponse) *ChatResponse {
	cr := &ChatResponse{
		ResponseID: resp.ID,
		Usage: Usage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		},
	}
	if resp.Usage.OutputTokensDetails != nil {
		cr.Usage.ReasoningTokens = resp.Usage.OutputTokensDetails.ReasoningTokens
	}

	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				if part.Type == "output_text" && part.Text != "" {
					cr.Content += part.Text
					// Append URL citations from annotations.
					if len(part.Annotations) > 0 {
						cr.Content += formatAnnotations(part.Annotations)
					}
				}
			}
		case "function_call":
			cr.ToolCalls = append(cr.ToolCalls, ToolCall{
				ID:        item.CallID,
				Name:      item.Name,
				Arguments: NormalizeArgs(item.Arguments),
			})
		case "web_search_call":
			// Web search calls are informational; no action needed from the agent.
		}
	}

	if len(cr.ToolCalls) > 0 {
		cr.FinishReason = "tool_calls"
	} else {
		cr.FinishReason = "stop"
	}

	return cr
}

// formatAnnotations formats URL citations from web search results into text.
func formatAnnotations(annotations []responsesAnnotation) string {
	var sb strings.Builder
	sb.WriteString("\n\nSources:")
	for _, a := range annotations {
		if a.Type == "url_citation" && a.URL != "" {
			title := a.Title
			if title == "" {
				title = a.URL
			}
			sb.WriteString("\n- [")
			sb.WriteString(title)
			sb.WriteString("](")
			sb.WriteString(a.URL)
			sb.WriteString(")")
		}
	}
	return sb.String()
}

// chatResponsesStream performs a streaming Responses API call.
func (c *openaiClient) chatResponsesStream(
	ctx context.Context, req ChatRequest,
) (<-chan StreamEvent, error) {
	wireReq := c.buildResponsesRequest(req, true)

	body, err := json.Marshal(wireReq)
	if err != nil {
		return nil, fmt.Errorf("openai responses: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/responses", bytes.NewReader(body))
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
	go c.readResponsesStream(ctx, resp.Body, ch)
	return ch, nil
}

// readResponsesStream parses Responses API SSE events into unified StreamEvents.
func (c *openaiClient) readResponsesStream(
	ctx context.Context, body io.ReadCloser,
	ch chan<- StreamEvent,
) {
	defer close(ch)
	defer body.Close()

	var responseID string
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

		switch ev.Event {
		case "response.created":
			var rc struct {
				Response struct {
					ID string `json:"id"`
				} `json:"response"`
			}
			if json.Unmarshal([]byte(ev.Data), &rc) == nil {
				responseID = rc.Response.ID
			}

		case "response.output_text.delta":
			var delta struct {
				Delta string `json:"delta"`
			}
			if json.Unmarshal([]byte(ev.Data), &delta) == nil && delta.Delta != "" {
				sendEvent(ctx, ch, StreamEvent{
					Type:    StreamContentDelta,
					Content: delta.Delta,
				})
			}

		case "response.function_call_arguments.delta":
			var delta struct {
				ItemID string `json:"item_id"`
				CallID string `json:"call_id"`
				Delta  string `json:"delta"`
			}
			if json.Unmarshal([]byte(ev.Data), &delta) == nil {
				sendEvent(ctx, ch, StreamEvent{
					Type: StreamToolCallDelta,
					ToolCall: &ToolCallDelta{
						ID:        delta.CallID,
						Arguments: delta.Delta,
					},
				})
			}

		case "response.output_item.added":
			var item struct {
				Item struct {
					Type   string `json:"type"`
					CallID string `json:"call_id"`
					Name   string `json:"name"`
				} `json:"item"`
			}
			if json.Unmarshal([]byte(ev.Data), &item) == nil && item.Item.Type == "function_call" {
				sendEvent(ctx, ch, StreamEvent{
					Type: StreamToolCallDelta,
					ToolCall: &ToolCallDelta{
						ID:   item.Item.CallID,
						Name: item.Item.Name,
					},
				})
			}

		case "response.completed":
			var completed struct {
				Response struct {
					ID    string         `json:"id"`
					Usage responsesUsage `json:"usage"`
				} `json:"response"`
			}
			if json.Unmarshal([]byte(ev.Data), &completed) == nil {
				responseID = completed.Response.ID
				usage = &Usage{
					PromptTokens:     completed.Response.Usage.InputTokens,
					CompletionTokens: completed.Response.Usage.OutputTokens,
					TotalTokens:      completed.Response.Usage.TotalTokens,
				}
				if completed.Response.Usage.OutputTokensDetails != nil {
					usage.ReasoningTokens = completed.Response.Usage.OutputTokensDetails.ReasoningTokens
				}
			}
			sendEvent(ctx, ch, StreamEvent{
				Type:       StreamDone,
				Usage:      usage,
				ResponseID: responseID,
			})
			return
		}
	}
}
