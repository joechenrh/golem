package llm

import (
	"context"
	"fmt"
	"net/http"
)

type openaiClient struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

func newOpenAIClient(apiKey, baseURL string) *openaiClient {
	return &openaiClient{
		apiKey:  apiKey,
		baseURL: baseURL,
		http:    &http.Client{},
	}
}

func (c *openaiClient) Provider() Provider {
	return ProviderOpenAI
}

func (c *openaiClient) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	return nil, fmt.Errorf("openai Chat: not yet implemented")
}

func (c *openaiClient) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	return nil, fmt.Errorf("openai ChatStream: not yet implemented")
}
