package middleware

import (
	"context"
	"strings"
	"testing"
)

func TestACL(t *testing.T) {
	passthrough := func(_ context.Context, args string) (string, error) {
		return "ok", nil
	}
	ctx := context.Background()

	tests := []struct {
		name      string
		allow     map[string]bool
		deny      map[string]bool
		tool      string
		wantError bool
	}{
		{
			name:      "no ACL allows all",
			tool:      "shell_exec",
			wantError: false,
		},
		{
			name:      "allow list permits listed tool",
			allow:     map[string]bool{"read_file": true, "write_file": true},
			tool:      "read_file",
			wantError: false,
		},
		{
			name:      "allow list blocks unlisted tool",
			allow:     map[string]bool{"read_file": true},
			tool:      "shell_exec",
			wantError: true,
		},
		{
			name:      "deny list blocks listed tool",
			deny:      map[string]bool{"shell_exec": true},
			tool:      "shell_exec",
			wantError: true,
		},
		{
			name:      "deny list allows unlisted tool",
			deny:      map[string]bool{"shell_exec": true},
			tool:      "read_file",
			wantError: false,
		},
		{
			name:      "deny takes precedence over allow",
			allow:     map[string]bool{"shell_exec": true},
			deny:      map[string]bool{"shell_exec": true},
			tool:      "shell_exec",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mw := ACL(tt.allow, tt.deny)
			result, err := mw(ctx, tt.tool, "{}", passthrough)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			gotError := strings.HasPrefix(result, "Error:")
			if gotError != tt.wantError {
				t.Errorf("got error=%v, want error=%v, result=%q", gotError, tt.wantError, result)
			}
		})
	}
}
