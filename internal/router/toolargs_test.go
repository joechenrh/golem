package router

import (
	"encoding/json"
	"testing"
)

func TestParseToolArgs(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want map[string]string
	}{
		{
			name: "empty",
			raw:  "",
			want: map[string]string{},
		},
		{
			name: "single unquoted",
			raw:  "path=test.txt",
			want: map[string]string{"path": "test.txt"},
		},
		{
			name: "single quoted",
			raw:  `command="ls -la"`,
			want: map[string]string{"command": "ls -la"},
		},
		{
			name: "multiple args",
			raw:  `command="ls -la" timeout=30`,
			want: map[string]string{"command": "ls -la", "timeout": "30"},
		},
		{
			name: "whitespace only",
			raw:  "   ",
			want: map[string]string{},
		},
		{
			name: "multiple unquoted",
			raw:  "path=foo.txt mode=read",
			want: map[string]string{"path": "foo.txt", "mode": "read"},
		},
		{
			name: "unclosed quote takes rest",
			raw:  `command="ls -la`,
			want: map[string]string{"command": "ls -la"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseToolArgs(tt.raw)

			var gotMap map[string]string
			if err := json.Unmarshal([]byte(got), &gotMap); err != nil {
				t.Fatalf("ParseToolArgs(%q) returned invalid JSON: %s", tt.raw, got)
			}

			if len(gotMap) != len(tt.want) {
				t.Errorf("ParseToolArgs(%q) = %s, want %d keys, got %d",
					tt.raw, got, len(tt.want), len(gotMap))
				return
			}
			for k, v := range tt.want {
				if gotMap[k] != v {
					t.Errorf("ParseToolArgs(%q)[%q] = %q, want %q",
						tt.raw, k, gotMap[k], v)
				}
			}
		})
	}
}
