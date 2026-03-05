package llm

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/time/rate"
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

// ClientFactory creates a Client for a given provider with the provided API key and base URL.
type ClientFactory func(apiKey, baseURL string) Client

var (
	providersMu sync.RWMutex
	providers   = map[Provider]struct {
		factory    ClientFactory
		defaultURL string
	}{
		ProviderOpenAI:    {factory: func(k, u string) Client { return newOpenAIClient(k, u) }, defaultURL: "https://api.openai.com/v1"},
		ProviderAnthropic: {factory: func(k, u string) Client { return newAnthropicClient(k, u) }, defaultURL: "https://api.anthropic.com"},
	}
)

// RegisterProvider registers a new LLM provider factory.
// This allows third-party providers to be plugged in without modifying this package.
func RegisterProvider(name Provider, defaultURL string, factory ClientFactory) {
	providersMu.Lock()
	defer providersMu.Unlock()
	providers[name] = struct {
		factory    ClientFactory
		defaultURL string
	}{factory: factory, defaultURL: defaultURL}
}

// NewClient creates a Client based on the provider string.
func NewClient(provider Provider, apiKey string, opts ...ClientOption) (Client, error) {
	o := &clientOptions{}
	for _, opt := range opts {
		opt(o)
	}

	providersMu.RLock()
	p, ok := providers[provider]
	providersMu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("unsupported LLM provider: %q", provider)
	}

	baseURL := p.defaultURL
	if o.baseURL != "" {
		baseURL = o.baseURL
	}
	return p.factory(apiKey, baseURL), nil
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

// RateLimitedClient wraps a Client with a token-bucket rate limiter to prevent
// bursts of API requests from exhausting provider rate limits.
type RateLimitedClient struct {
	inner   Client
	limiter *rate.Limiter
}

// NewRateLimitedClient creates a Client that rate-limits LLM calls to rps
// requests per second with a burst size equal to rps. If rps <= 0, no
// limiting is applied.
func NewRateLimitedClient(inner Client, rps int) Client {
	if rps <= 0 {
		return inner
	}
	return &RateLimitedClient{
		inner:   inner,
		limiter: rate.NewLimiter(rate.Limit(rps), rps),
	}
}

func (r *RateLimitedClient) Provider() Provider { return r.inner.Provider() }

func (r *RateLimitedClient) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	return r.inner.Chat(ctx, req)
}

func (r *RateLimitedClient) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	return r.inner.ChatStream(ctx, req)
}
