package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

func TestLoadExternalTools_ValidManifest(t *testing.T) {
	dir := t.TempDir()

	manifest := ExternalToolManifest{
		Name:        "test_tool",
		Description: "A test tool",
		Command:     "/bin/echo",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}}}`),
	}
	data, _ := json.Marshal(manifest)
	os.WriteFile(filepath.Join(dir, "test.tool.json"), data, 0644)

	tools, err := LoadExternalTools(dir, zap.NewNop())
	if err != nil {
		t.Fatalf("LoadExternalTools: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name() != "test_tool" {
		t.Errorf("expected name 'test_tool', got %q", tools[0].Name())
	}
}

func TestLoadExternalTools_MissingDir(t *testing.T) {
	tools, err := LoadExternalTools("/nonexistent/path", zap.NewNop())
	if err != nil {
		t.Fatalf("expected nil error for missing dir, got: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(tools))
	}
}

func TestLoadExternalTools_InvalidManifest(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "bad.tool.json"), []byte("{invalid json}"), 0644)

	_, err := LoadExternalTools(dir, zap.NewNop())
	if err == nil {
		t.Fatal("expected error for invalid JSON manifest")
	}
}

func TestLoadExternalTools_MissingRequiredFields(t *testing.T) {
	dir := t.TempDir()

	// Missing command.
	manifest := ExternalToolManifest{Name: "test_tool"}
	data, _ := json.Marshal(manifest)
	os.WriteFile(filepath.Join(dir, "test.tool.json"), data, 0644)

	_, err := LoadExternalTools(dir, zap.NewNop())
	if err == nil {
		t.Fatal("expected error for missing command")
	}
}

func TestExternalTool_ExecuteEnv(t *testing.T) {
	// Verify that manifest env vars are passed to the child process.
	script := `#!/bin/sh
read line
printf '{"jsonrpc":"2.0","id":1,"result":"%s"}' "$TEST_SECRET"
`
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "env_plugin.sh")
	os.WriteFile(scriptPath, []byte(script), 0755)

	tool := NewExternalTool(ExternalToolManifest{
		Name:        "env_tool",
		Description: "reads env",
		Command:     "/bin/sh",
		Args:        []string{scriptPath},
		Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
		Env:         map[string]string{"TEST_SECRET": "s3cret"},
	}, zap.NewNop())
	defer tool.Close()

	result, err := tool.Execute(context.Background(), "{}")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result != "s3cret" {
		t.Errorf("expected 's3cret', got %q", result)
	}
}

func TestExternalTool_ExecuteEcho(t *testing.T) {
	// Create a tool that runs a simple echo script via sh.
	// The script reads a JSON-RPC request from stdin and returns a response.
	script := `#!/bin/sh
read line
echo '{"jsonrpc":"2.0","id":1,"result":"hello from plugin"}'
`
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "plugin.sh")
	os.WriteFile(scriptPath, []byte(script), 0755)

	tool := NewExternalTool(ExternalToolManifest{
		Name:        "echo_tool",
		Description: "echoes back",
		Command:     "/bin/sh",
		Args:        []string{scriptPath},
		Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
	}, zap.NewNop())
	defer tool.Close()

	result, err := tool.Execute(context.Background(), "{}")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result != "hello from plugin" {
		t.Errorf("expected 'hello from plugin', got %q", result)
	}
}

func TestExternalTool_ExecuteError(t *testing.T) {
	script := `#!/bin/sh
read line
echo '{"jsonrpc":"2.0","id":1,"error":{"code":-1,"message":"something went wrong"}}'
`
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "plugin.sh")
	os.WriteFile(scriptPath, []byte(script), 0755)

	tool := NewExternalTool(ExternalToolManifest{
		Name:        "error_tool",
		Description: "returns an error",
		Command:     "/bin/sh",
		Args:        []string{scriptPath},
		Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
	}, zap.NewNop())
	defer tool.Close()

	result, err := tool.Execute(context.Background(), "{}")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result != "Error: something went wrong (code -1)" {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestIsToolManifest(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"test.tool.json", true},
		{"my-plugin.tool.json", true},
		{".tool.json", false},
		{"test.json", false},
		{"tool.json", false},
		{"README.md", false},
	}
	for _, tc := range cases {
		if got := isToolManifest(tc.name); got != tc.want {
			t.Errorf("isToolManifest(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}
