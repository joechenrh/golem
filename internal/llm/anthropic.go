package llm

import (
	"context"
	"fmt"
	"net/http"
)

type anthropicClient struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

func newAnthropicClient(apiKey, baseURL string) *anthropicClient {
	return &anthropicClient{
		apiKey:  apiKey,
		baseURL: baseURL,
		http:    &http.Client{},
	}
}

func (c *anthropicClient) Provider() Provider {
	return ProviderAnthropic
}

func (c *anthropicClient) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	return nil, fmt.Errorf("anthropic Chat: not yet implemented")
}

func (c *anthropicClient) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	return nil, fmt.Errorf("anthropic ChatStream: not yet implemented")
}
