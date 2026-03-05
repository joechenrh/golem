package middleware

import "context"

// Middleware wraps tool execution with cross-cutting behavior.
// Call next(ctx, args) to proceed to the next middleware or the actual tool.
type Middleware func(ctx context.Context, toolName string, args string, next func(context.Context, string) (string, error)) (string, error)
