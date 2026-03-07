package middleware

import (
	"context"
	"fmt"
)

// ACL returns a middleware that enforces per-tool access control.
// If allow is non-empty, only tools in the allow set may execute (allowlist mode).
// If deny is non-empty, tools in the deny set are blocked (denylist mode).
// Deny takes precedence: a tool in both sets is blocked.
func ACL(allow, deny map[string]bool) Middleware {
	return func(ctx context.Context, toolName string, args string, next func(context.Context, string) (string, error)) (string, error) {
		if deny[toolName] {
			return fmt.Sprintf("Error: tool %q is not allowed by access policy", toolName), nil
		}
		if len(allow) > 0 && !allow[toolName] {
			return fmt.Sprintf("Error: tool %q is not allowed by access policy", toolName), nil
		}
		return next(ctx, args)
	}
}
