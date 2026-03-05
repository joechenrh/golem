package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSpawnAgentTool_Execute(t *testing.T) {
	runner := func(_ context.Context, prompt string) (string, error) {
		return "sub-agent response to: " + prompt, nil
	}
	tool := NewSpawnAgentTool(runner)

	if tool.Name() != "spawn_agent" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "spawn_agent")
	}

	args, _ := json.Marshal(map[string]string{"prompt": "Hello sub-agent"})
	result, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(result, "Hello sub-agent") {
		t.Errorf("result = %q, want to contain prompt", result)
	}
}

func TestSpawnAgentTool_EmptyPrompt(t *testing.T) {
	runner := func(_ context.Context, prompt string) (string, error) {
		return prompt, nil
	}
	tool := NewSpawnAgentTool(runner)

	args, _ := json.Marshal(map[string]string{"prompt": ""})
	result, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(result, "required") {
		t.Errorf("result = %q, want error about required prompt", result)
	}
}

func TestSpawnAgentTool_Parameters(t *testing.T) {
	tool := NewSpawnAgentTool(nil)

	var schema struct {
		Type       string         `json:"type"`
		Required   []string       `json:"required"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(tool.Parameters(), &schema); err != nil {
		t.Fatalf("Parameters() invalid JSON: %v", err)
	}
	if schema.Type != "object" {
		t.Errorf("schema type = %q, want %q", schema.Type, "object")
	}
	if len(schema.Required) != 1 || schema.Required[0] != "prompt" {
		t.Errorf("required = %v, want [prompt]", schema.Required)
	}
	if _, ok := schema.Properties["prompt"]; !ok {
		t.Error("schema missing 'prompt' property")
	}
}
