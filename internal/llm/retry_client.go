package llm

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"
)

// RetryClient wraps a Client and adds retry with exponential backoff to
// ChatStream calls. The non-streaming Chat path already retries at the
// HTTP transport layer (doWithRetry); this wrapper provides equivalent
// resilience for the streaming path, reusing the shared backoff and
// retryable-status logic from retry.go.
//
// Only connection-time errors and retryable API errors (429, 5xx) are
// retried. Once a stream is established and events start flowing, errors
// are NOT retried because partial content has already been delivered.
type RetryClient struct {
	inner Client
	cfg   retryConfig
}

// NewRetryClient wraps inner with streaming retry. If maxAttempts <= 0,
// defaults to 3.
func NewRetryClient(inner Client, maxAttempts int, baseBackoff time.Duration, logger *zap.Logger) Client {
	cfg := retryConfig{
		maxAttempts: maxAttempts,
		baseBackoff: baseBackoff,
		logger:      logger,
	}
	if cfg.maxAttempts <= 0 {
		cfg.maxAttempts = defaultMaxAttempts
	}
	if cfg.baseBackoff <= 0 {
		cfg.baseBackoff = defaultBaseBackoff
	}
	return &RetryClient{inner: inner, cfg: cfg}
}

func (r *RetryClient) Provider() Provider { return r.inner.Provider() }

func (r *RetryClient) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	return r.inner.Chat(ctx, req)
}

// ChatStream retries the initial connection on retryable errors.
func (r *RetryClient) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	var lastErr error
	for attempt := range r.cfg.maxAttempts {
		ch, err := r.inner.ChatStream(ctx, req)
		if err == nil {
			return ch, nil
		}

		if !isRetryableErr(err) {
			return nil, err
		}

		lastErr = err
		if attempt < r.cfg.maxAttempts-1 {
			if waitErr := backoff(ctx, r.cfg, attempt, nil); waitErr != nil {
				return nil, waitErr
			}
		}
	}
	return nil, lastErr
}

// isRetryableErr returns true for network errors and retryable API errors.
func isRetryableErr(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return isRetryableStatus(apiErr.StatusCode)
	}
	// Network errors are retryable.
	return true
}
