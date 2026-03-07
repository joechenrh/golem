package builtin

import (
	"encoding/json"
	"testing"
)

func TestLarkSendTool_Metadata(t *testing.T) {
	// NewLarkSendTool requires non-nil args, but we can test metadata without a real channel.
	tool := &LarkSendTool{}

	if tool.Name() != "lark_send" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "lark_send")
	}

	var schema struct {
		Type       string         `json:"type"`
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	if err := json.Unmarshal(tool.Parameters(), &schema); err != nil {
		t.Fatalf("Parameters() invalid JSON: %v", err)
	}
	if schema.Type != "object" {
		t.Errorf("schema type = %q, want %q", schema.Type, "object")
	}
	if _, ok := schema.Properties["chat_id"]; !ok {
		t.Error("schema missing 'chat_id' property")
	}
	if _, ok := schema.Properties["message"]; !ok {
		t.Error("schema missing 'message' property")
	}
	if _, ok := schema.Properties["image"]; !ok {
		t.Error("schema missing 'image' property")
	}
	if len(schema.Required) != 1 || schema.Required[0] != "chat_id" {
		t.Errorf("required = %v, want [chat_id]", schema.Required)
	}
}

func TestLarkSendTool_Validation(t *testing.T) {
	tool := &LarkSendTool{}

	tests := []struct {
		name    string
		args    string
		wantErr string
	}{
		{
			name:    "missing chat_id",
			args:    `{"message":"hello"}`,
			wantErr: "'chat_id' is required",
		},
		{
			name:    "missing both message and image",
			args:    `{"chat_id":"chat123"}`,
			wantErr: "at least one of 'message' or 'image' must be provided",
		},
		{
			name:    "invalid JSON",
			args:    `{bad}`,
			wantErr: "invalid arguments",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tool.Execute(t.Context(), tt.args)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result == "" || !contains(result, tt.wantErr) {
				t.Errorf("result = %q, want to contain %q", result, tt.wantErr)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
