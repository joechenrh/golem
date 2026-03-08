package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joechenrh/golem/internal/tools"
)

func TestCreateSkillTool(t *testing.T) {
	tests := []struct {
		name      string
		agentName string
		args      string
		wantErr   string // substring that must appear in result
		wantOK    string // substring that must appear in result on success
	}{
		{
			name:      "basic create",
			agentName: "test-agent",
			args:      `{"name":"greet","description":"Say hello","body":"# Greet\nSay hello to the user."}`,
			wantOK:    `Skill "greet" created and registered as skill_greet.`,
		},
		{
			name:      "update existing",
			agentName: "test-agent",
			args:      `{"name":"greet","description":"Say hi updated","body":"# Greet v2\nSay hi."}`,
			wantOK:    `Skill "greet" created and registered as skill_greet.`,
		},
		{
			name:      "invalid name - starts with hyphen",
			agentName: "test-agent",
			args:      `{"name":"-bad","description":"desc","body":"body"}`,
			wantErr:   "Error: name must match",
		},
		{
			name:      "invalid name - spaces",
			agentName: "test-agent",
			args:      `{"name":"has space","description":"desc","body":"body"}`,
			wantErr:   "Error: name must match",
		},
		{
			name:      "no agent name",
			agentName: "",
			args:      `{"name":"foo","description":"desc","body":"body"}`,
			wantErr:   "Error: create_skill requires a named agent",
		},
		{
			name:      "empty name",
			agentName: "test-agent",
			args:      `{"name":"","description":"desc","body":"body"}`,
			wantErr:   "Error: name is required",
		},
		{
			name:      "empty description",
			agentName: "test-agent",
			args:      `{"name":"foo","description":"","body":"body"}`,
			wantErr:   "Error: description is required",
		},
		{
			name:      "empty body",
			agentName: "test-agent",
			args:      `{"name":"foo","description":"desc","body":""}`,
			wantErr:   "Error: body is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use a temp dir as golem home so tests don't touch the real filesystem.
			tmpHome := t.TempDir()
			t.Setenv("HOME", tmpHome)

			registry := tools.NewRegistry()
			tool := NewCreateSkillTool(tt.agentName, registry)

			result, err := tool.Execute(nil, tt.args)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantErr != "" {
				if !strings.Contains(result, tt.wantErr) {
					t.Errorf("result = %q, want error containing %q", result, tt.wantErr)
				}
				return
			}

			if !strings.Contains(result, tt.wantOK) {
				t.Errorf("result = %q, want containing %q", result, tt.wantOK)
			}

			// Verify the file was written.
			skillPath := filepath.Join(tmpHome, ".golem", "agents", tt.agentName, "skills", "greet", "SKILL.md")
			data, err := os.ReadFile(skillPath)
			if err != nil {
				t.Fatalf("skill file not found: %v", err)
			}
			if !strings.Contains(string(data), "---") {
				t.Error("skill file missing frontmatter")
			}

			// Verify the skill was registered.
			if registry.Get("skill_greet") == nil {
				t.Error("skill_greet not registered in registry")
			}
		})
	}
}
