package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/joechenrh/golem/internal/tools"
)

func TestSpawnAgentTool_SyncFallback(t *testing.T) {
	creator := func(_ context.Context) (*SubAgentSession, error) {
		return &SubAgentSession{
			Runner: func(_ context.Context, prompt string) (string, error) {
				return "sub-agent response to: " + prompt, nil
			},
		}, nil
	}
	tool := NewSpawnAgentTool(creator)

	if tool.Name() != "spawn_agent" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "spawn_agent")
	}

	// No tracker in context -> sync fallback.
	args, _ := json.Marshal(map[string]string{"prompt": "Hello sub-agent"})
	result, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(result, "Hello sub-agent") {
		t.Errorf("result = %q, want to contain prompt", result)
	}
}

func TestSpawnAgentTool_AsyncWithTracker(t *testing.T) {
	var capturedPrompt string
	creator := func(_ context.Context) (*SubAgentSession, error) {
		return &SubAgentSession{
			Tracker: &mockTracker{},
			Runner: func(_ context.Context, prompt string) (string, error) {
				capturedPrompt = prompt
				return "done", nil
			},
		}, nil
	}
	tool := NewSpawnAgentTool(creator)

	// Create a mock tracker to verify async path.
	tracker := &mockTracker{}
	ctx := tools.WithTaskTracker(context.Background(), tracker)

	args, _ := json.Marshal(map[string]string{"prompt": "Fix the bug"})
	result, err := tool.Execute(ctx, string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	// Should return immediately with task ID.
	if !strings.Contains(result, "Task #") {
		t.Errorf("result = %q, want task ID", result)
	}
	// Verify Launch was called.
	if tracker.launchCount != 1 {
		t.Errorf("Launch called %d times, want 1", tracker.launchCount)
	}

	// Execute the captured function to verify it calls the runner
	// and links the child tracker.
	if tracker.lastFn != nil {
		tracker.lastFn(context.Background(), 1)
		if capturedPrompt != "Fix the bug" {
			t.Errorf("runner got prompt %q, want %q", capturedPrompt, "Fix the bug")
		}
		if tracker.childTrackerID != 1 {
			t.Errorf("SetChildTracker called with id %d, want 1", tracker.childTrackerID)
		}
	}
}

func TestSpawnAgentTool_EmptyPrompt(t *testing.T) {
	creator := func(_ context.Context) (*SubAgentSession, error) {
		return &SubAgentSession{
			Runner: func(_ context.Context, prompt string) (string, error) {
				return prompt, nil
			},
		}, nil
	}
	tool := NewSpawnAgentTool(creator)

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
	tool := NewSpawnAgentTool(func(_ context.Context) (*SubAgentSession, error) { return nil, nil })

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

func TestSpawnAgentTool_Description(t *testing.T) {
	creator := func(_ context.Context) (*SubAgentSession, error) {
		return &SubAgentSession{
			Tracker: &mockTracker{},
			Runner: func(_ context.Context, prompt string) (string, error) {
				return "done", nil
			},
		}, nil
	}
	tool := NewSpawnAgentTool(creator)

	tracker := &mockTracker{}
	ctx := tools.WithTaskTracker(context.Background(), tracker)

	args, _ := json.Marshal(map[string]string{
		"prompt":      "Fix the authentication bug in login.go by checking token expiry",
		"description": "fix auth bug",
	})
	result, err := tool.Execute(ctx, string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(result, "fix auth bug") {
		t.Errorf("result should contain description, got: %s", result)
	}
}

// mockTracker implements tools.BackgroundTaskTracker for testing.
type mockTracker struct {
	launchCount    int
	lastFn         func(ctx context.Context, id int)
	childTrackerID int
}

func (m *mockTracker) Launch(desc string, fn func(ctx context.Context, id int)) int {
	m.launchCount++
	m.lastFn = fn
	return m.launchCount
}

func (m *mockTracker) Complete(int, string)      {}
func (m *mockTracker) Fail(int, string)          {}
func (m *mockTracker) TreeSummary(string) string { return "" }

func (m *mockTracker) SetChildTracker(id int, _ tools.BackgroundTaskTracker) {
	m.childTrackerID = id
}
