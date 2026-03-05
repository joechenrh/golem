package builtin

import (
	"encoding/json"
	"testing"
)

func TestExtractToken(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "bare token",
			input: "ABC123",
			want:  "ABC123",
		},
		{
			name:  "docx URL",
			input: "https://example.feishu.cn/docx/ABC123",
			want:  "ABC123",
		},
		{
			name:  "wiki URL",
			input: "https://example.feishu.cn/wiki/XYZ789",
			want:  "XYZ789",
		},
		{
			name:  "sheets URL",
			input: "https://example.feishu.cn/sheets/DEF456",
			want:  "DEF456",
		},
		{
			name:  "base URL",
			input: "https://example.feishu.cn/base/GHI012",
			want:  "GHI012",
		},
		{
			name:  "larksuite URL",
			input: "https://example.larksuite.com/docx/TOKEN99",
			want:  "TOKEN99",
		},
		{
			name:  "URL with trailing slash",
			input: "https://example.feishu.cn/docx/ABC123/",
			want:  "ABC123",
		},
		{
			name:  "URL with query params",
			input: "https://example.feishu.cn/docx/ABC123?from=share",
			want:  "ABC123",
		},
		{
			name:  "whitespace padding",
			input: "  ABC123  ",
			want:  "ABC123",
		},
		{
			name:  "short URL with only two segments",
			input: "https://example.feishu.cn/docx",
			want:  "https://example.feishu.cn/docx",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractToken(tt.input)
			if got != tt.want {
				t.Errorf("extractToken(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestLarkReadDocToolMetadata(t *testing.T) {
	tool := &LarkReadDocTool{}

	if tool.Name() != "lark_read_doc" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "lark_read_doc")
	}
	if tool.Description() == "" {
		t.Error("Description() should not be empty")
	}
	if tool.FullDescription() == "" {
		t.Error("FullDescription() should not be empty")
	}
	if tool.Parameters() == nil {
		t.Error("Parameters() should not be nil")
	}

	// Verify parameters JSON is valid.
	var params map[string]any
	if err := json.Unmarshal(tool.Parameters(), &params); err != nil {
		t.Errorf("Parameters() is not valid JSON: %v", err)
	}
}

func TestLarkWriteDocToolMetadata(t *testing.T) {
	tool := &LarkWriteDocTool{}

	if tool.Name() != "lark_write_doc" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "lark_write_doc")
	}
	if tool.Description() == "" {
		t.Error("Description() should not be empty")
	}
	if tool.FullDescription() == "" {
		t.Error("FullDescription() should not be empty")
	}
	if tool.Parameters() == nil {
		t.Error("Parameters() should not be nil")
	}

	// Verify parameters JSON is valid and has required fields.
	var params map[string]any
	if err := json.Unmarshal(tool.Parameters(), &params); err != nil {
		t.Errorf("Parameters() is not valid JSON: %v", err)
	}
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatal("Parameters() missing 'properties'")
	}
	if _, ok := props["document_id"]; !ok {
		t.Error("Parameters() missing 'document_id' property")
	}
	if _, ok := props["content"]; !ok {
		t.Error("Parameters() missing 'content' property")
	}
}
