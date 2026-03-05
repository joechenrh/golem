package middleware

import (
	"context"

	"github.com/joechenrh/golem/internal/redact"
)

// Redact returns a Middleware that redacts secrets from tool results.
func Redact(r *redact.Redactor) Middleware {
	return func(ctx context.Context, toolName, args string,
		next func(context.Context, string) (string, error)) (string, error) {
		result, err := next(ctx, args)
		if err != nil {
			return result, err
		}
		return r.Redact(result), nil
	}
}
