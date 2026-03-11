package llm

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"go.uber.org/zap"
)

// RetryClient wraps a Client and adds retry with exponential backoff to
// ChatStream calls. The non-streaming Chat path already retries at the
// HTTP transport layer (doWithRetry); this wrapper provides equivalent
// resilience for the streaming path.
//
// Only connection-time errors and retryable API errors (429, 5xx) are
// retried. Once a stream is established and events start flowing, errors
// are NOT retried because partial content has already been delivered.
type RetryClient struct {
	inner       Client
	maxAttempts int
	baseBackoff time.Duration
	logger      *zap.Logger
}

// NewRetryClient wraps inner with streaming retry. If maxAttempts <= 0,
// defaults to 3.
func NewRetryClient(inner Client, maxAttempts int, baseBackoff time.Duration, logger *zap.Logger) Client {
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxAttempts
	}
	if baseBackoff <= 0 {
		baseBackoff = defaultBaseBackoff
	}
	return &RetryClient{
		inner:       inner,
		maxAttempts: maxAttempts,
		baseBackoff: baseBackoff,
		logger:      logger,
	}
}

func (r *RetryClient) Provider() Provider { return r.inner.Provider() }

func (r *RetryClient) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	return r.inner.Chat(ctx, req)
}

// ChatStream retries the initial connection on retryable errors.
func (r *RetryClient) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	var lastErr error
	for attempt := range r.maxAttempts {
		ch, err := r.inner.ChatStream(ctx, req)
		if err == nil {
			return ch, nil
		}

		if !r.isRetryable(err) {
			return nil, err
		}

		lastErr = err
		if attempt < r.maxAttempts-1 {
			wait := r.backoffDuration(attempt)
			if r.logger != nil {
				r.logger.Warn("retrying ChatStream",
					zap.Int("attempt", attempt+1),
					zap.Int("max", r.maxAttempts),
					zap.Duration("backoff", wait),
					zap.Error(err))
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}
	}
	return nil, lastErr
}

// isRetryable returns true for network errors and retryable API errors.
func (r *RetryClient) isRetryable(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == 429 || apiErr.StatusCode >= 500
	}
	// Network errors are retryable.
	return true
}

// backoffDuration returns the wait duration for the given attempt with jitter.
func (r *RetryClient) backoffDuration(attempt int) time.Duration {
	wait := min(r.baseBackoff*(1<<uint(attempt)), maxBackoffCap)
	jitter := 0.5 + rand.Float64()
	return time.Duration(float64(wait) * jitter)
}
