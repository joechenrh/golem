package redact

import (
	"context"

	"github.com/joechenrh/golem/internal/tools"
)

// Middleware returns a tools.Middleware that redacts secrets from tool results.
func Middleware(r *Redactor) tools.Middleware {
	return func(ctx context.Context, toolName, args string,
		next func(context.Context, string) (string, error)) (string, error) {
		result, err := next(ctx, args)
		if err != nil {
			return result, err
		}
		return r.Redact(result), nil
	}
}
