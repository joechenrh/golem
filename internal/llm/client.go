package llm

import (
	"context"
	"fmt"
	"strings"
)

// Client is the unified interface for calling LLMs.
type Client interface {
	// Chat sends a non-streaming chat completion request.
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)

	// ChatStream sends a streaming chat completion request.
	// Events are sent on the returned channel until it is closed.
	ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)

	// Provider returns which provider this client talks to.
	Provider() Provider
}

// APIError represents an error from an LLM provider API.
type APIError struct {
	StatusCode int
	Message    string
	Provider   Provider
	Retryable  bool
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s API error (HTTP %d): %s", e.Provider, e.StatusCode, e.Message)
}

// NewClient creates a Client based on the provider string.
func NewClient(provider Provider, apiKey string, opts ...ClientOption) (Client, error) {
	o := &clientOptions{}
	for _, opt := range opts {
		opt(o)
	}

	switch provider {
	case ProviderOpenAI:
		baseURL := "https://api.openai.com/v1"
		if o.baseURL != "" {
			baseURL = o.baseURL
		}
		return newOpenAIClient(apiKey, baseURL), nil
	case ProviderAnthropic:
		baseURL := "https://api.anthropic.com"
		if o.baseURL != "" {
			baseURL = o.baseURL
		}
		return newAnthropicClient(apiKey, baseURL), nil
	default:
		return nil, fmt.Errorf("unsupported LLM provider: %q", provider)
	}
}

// ParseModelProvider splits a model string like "openai:gpt-4o" into (provider, model).
// If no prefix, defaults to OpenAI.
func ParseModelProvider(model string) (Provider, string) {
	parts := strings.SplitN(model, ":", 2)
	if len(parts) == 2 {
		return Provider(parts[0]), parts[1]
	}
	return ProviderOpenAI, model
}

// ClientOption configures a Client.
type ClientOption func(*clientOptions)

type clientOptions struct {
	baseURL string
}

// WithBaseURL overrides the default API base URL.
func WithBaseURL(url string) ClientOption {
	return func(o *clientOptions) {
		o.baseURL = url
	}
}
