package llm

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"go.uber.org/zap"
)

const (
	defaultMaxAttempts = 3 // 1 initial + 2 retries
	defaultBaseBackoff = 1 * time.Second
	maxBackoffCap      = 30 * time.Second
	maxErrorBodyLen    = 200
)

type retryConfig struct {
	maxAttempts int           // default 3 (1 initial + 2 retries)
	baseBackoff time.Duration // default 1s, tests use 1ms
	logger      *zap.Logger   // nil means no logging
}

func defaultRetryConfig() retryConfig {
	return retryConfig{
		maxAttempts: defaultMaxAttempts,
		baseBackoff: defaultBaseBackoff,
	}
}

// doWithRetry executes fn with exponential backoff and jitter for retryable failures.
func doWithRetry(
	ctx context.Context, cfg retryConfig,
	fn func() (*http.Response, error),
) (*http.Response, error) {
	if cfg.maxAttempts <= 0 {
		cfg.maxAttempts = defaultMaxAttempts
	}
	if cfg.baseBackoff <= 0 {
		cfg.baseBackoff = defaultBaseBackoff
	}

	var lastErr error
	for attempt := range cfg.maxAttempts {
		resp, err := fn()
		if err != nil {
			// Network error — retryable.
			lastErr = err
			if attempt < cfg.maxAttempts-1 {
				if waitErr := backoff(ctx, cfg, attempt, nil); waitErr != nil {
					return nil, waitErr
				}
			}
			continue
		}

		// Success.
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, nil
		}

		// Not retryable: 4xx except 429.
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 429 {
			return resp, nil
		}

		// Retryable: 429 or 5xx. Read body before closing for error context.
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		lastErr = &APIError{
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("retryable error (attempt %d/%d): %s", attempt+1, cfg.maxAttempts, truncateBody(body, maxErrorBodyLen)),
			Retryable:  true,
		}

		if attempt < cfg.maxAttempts-1 {
			if waitErr := backoff(ctx, cfg, attempt, resp); waitErr != nil {
				return nil, waitErr
			}
		}
	}

	return nil, lastErr
}

// backoff sleeps with exponential backoff and jitter. Respects Retry-After header on 429 responses.
func backoff(
	ctx context.Context, cfg retryConfig,
	attempt int, resp *http.Response,
) error {
	wait := cfg.baseBackoff * (1 << uint(attempt))

	if wait > maxBackoffCap {
		wait = maxBackoffCap
	}

	// Respect Retry-After header if present.
	if resp != nil && resp.StatusCode == 429 {
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil {
				raWait := time.Duration(secs) * time.Second
				if raWait > wait {
					wait = raWait
				}
			}
		}
	}

	// Apply jitter: multiply by random factor in [0.5, 1.5).
	jitter := 0.5 + rand.Float64() // [0.5, 1.5) via math/rand/v2
	wait = time.Duration(float64(wait) * jitter)

	if cfg.logger != nil {
		fields := []zap.Field{
			zap.Int("attempt", attempt+1),
			zap.Int("max", cfg.maxAttempts),
			zap.Duration("backoff", wait),
		}
		if resp != nil {
			fields = append(fields, zap.Int("status", resp.StatusCode))
		}
		cfg.logger.Warn("retrying LLM request", fields...)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

// truncateBody returns a string from body bytes, truncating to maxLen.
func truncateBody(body []byte, maxLen int) string {
	s := string(body)
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}
