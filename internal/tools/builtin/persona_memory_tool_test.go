package builtin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersonaMemoryTool_ReadEmpty(t *testing.T) {
	dir := resolvedTempDir(t)
	memPath := filepath.Join(dir, "MEMORY.md")

	tool := NewPersonaMemoryTool(memPath)
	result, err := tool.Execute(context.Background(), `{"action":"read"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "does not exist") {
		t.Errorf("result = %q, want 'does not exist'", result)
	}
}

func TestPersonaMemoryTool_WriteAndRead(t *testing.T) {
	dir := resolvedTempDir(t)
	memPath := filepath.Join(dir, "MEMORY.md")

	tool := NewPersonaMemoryTool(memPath)

	// Write.
	result, err := tool.Execute(context.Background(), `{"action":"write","content":"# Memory\n\nUser prefers short answers."}`)
	if err != nil {
		t.Fatalf("write Execute: %v", err)
	}
	if !strings.Contains(result, "updated") {
		t.Errorf("write result = %q, want success", result)
	}

	// Read back.
	result, err = tool.Execute(context.Background(), `{"action":"read"}`)
	if err != nil {
		t.Fatalf("read Execute: %v", err)
	}
	if !strings.Contains(result, "User prefers short answers.") {
		t.Errorf("read result = %q, want memory content", result)
	}
}

func TestPersonaMemoryTool_WriteCreatesDir(t *testing.T) {
	dir := resolvedTempDir(t)
	memPath := filepath.Join(dir, "nested", "deep", "MEMORY.md")

	tool := NewPersonaMemoryTool(memPath)
	result, err := tool.Execute(context.Background(), `{"action":"write","content":"test"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "updated") {
		t.Errorf("result = %q", result)
	}

	data, _ := os.ReadFile(memPath)
	if string(data) != "test" {
		t.Errorf("file content = %q, want %q", data, "test")
	}
}

func TestPersonaMemoryTool_WriteEmptyContent(t *testing.T) {
	dir := resolvedTempDir(t)
	memPath := filepath.Join(dir, "MEMORY.md")

	tool := NewPersonaMemoryTool(memPath)
	_, err := tool.Execute(context.Background(), `{"action":"write","content":""}`)
	if err == nil {
		t.Fatal("expected error for empty content")
	}
}

func TestPersonaMemoryTool_InvalidAction(t *testing.T) {
	dir := resolvedTempDir(t)
	tool := NewPersonaMemoryTool(filepath.Join(dir, "MEMORY.md"))

	_, err := tool.Execute(context.Background(), `{"action":"delete"}`)
	if err == nil {
		t.Fatal("expected error for invalid action")
	}
}

func TestPersonaMemoryTool_InvalidJSON(t *testing.T) {
	dir := resolvedTempDir(t)
	tool := NewPersonaMemoryTool(filepath.Join(dir, "MEMORY.md"))

	_, err := tool.Execute(context.Background(), `not json`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestPersonaMemoryTool_Metadata(t *testing.T) {
	tool := NewPersonaMemoryTool("/tmp/test.md")

	if tool.Name() != "persona_memory" {
		t.Errorf("Name() = %q", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description() is empty")
	}
	if tool.FullDescription() == "" {
		t.Error("FullDescription() is empty")
	}
	if tool.Parameters() == nil {
		t.Error("Parameters() is nil")
	}
}

func TestPersonaMemoryTool_ReadEmptyFile(t *testing.T) {
	dir := resolvedTempDir(t)
	memPath := filepath.Join(dir, "MEMORY.md")
	os.WriteFile(memPath, []byte(""), 0o644)

	tool := NewPersonaMemoryTool(memPath)
	result, err := tool.Execute(context.Background(), `{"action":"read"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "empty") {
		t.Errorf("result = %q, want 'empty'", result)
	}
}
