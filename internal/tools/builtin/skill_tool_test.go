package builtin

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joechenrh/golem/internal/tools"
)

// skillTestMockTool is a minimal Tool implementation for skill tool tests.
type skillTestMockTool struct {
	name     string
	desc     string
	fullDesc string
}

func (m *skillTestMockTool) Name() string            { return m.name }
func (m *skillTestMockTool) Description() string     { return m.desc }
func (m *skillTestMockTool) FullDescription() string { return m.fullDesc }
func (m *skillTestMockTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}}}`)
}
func (m *skillTestMockTool) Execute(_ context.Context, _ string) (string, error) {
	return "mock", nil
}

func TestSkillTool_Execute_Found(t *testing.T) {
	store := tools.NewSkillStore()
	store.Discover(filepath.Join("..", "..", "tools", "testdata", "skills"))

	registry := tools.NewRegistry()
	registry.Register(&skillTestMockTool{"read_file", "Read", "Read full"})
	registry.Register(&skillTestMockTool{"write_file", "Write", "Write full"})

	st := NewSkillTool(store, registry)

	result, err := st.Execute(context.Background(), `{"name":"my-skill"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(result, "My Skill Instructions") {
		t.Errorf("result should contain skill body, got: %s", result)
	}
}

func TestSkillTool_Execute_NotFound(t *testing.T) {
	store := tools.NewSkillStore()
	st := NewSkillTool(store, nil)

	result, err := st.Execute(context.Background(), `{"name":"nonexistent"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(result, "Error:") {
		t.Errorf("expected error message, got: %s", result)
	}
}

func TestSkillTool_Execute_EmptyName(t *testing.T) {
	store := tools.NewSkillStore()
	st := NewSkillTool(store, nil)

	result, err := st.Execute(context.Background(), `{"name":""}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(result, "Error: name is required") {
		t.Errorf("expected name required error, got: %s", result)
	}
}

func TestSkillTool_Name(t *testing.T) {
	st := NewSkillTool(nil, nil)
	if st.Name() != "skill" {
		t.Errorf("Name() = %q, want %q", st.Name(), "skill")
	}
}

func TestSkillTool_FullDescription_IncludesSkills(t *testing.T) {
	store := tools.NewSkillStore()
	store.Discover(filepath.Join("..", "..", "tools", "testdata", "skills"))

	st := NewSkillTool(store, nil)
	desc := st.FullDescription()
	if !strings.Contains(desc, "Available skills:") {
		t.Errorf("FullDescription() should list available skills, got: %s", desc)
	}
	if !strings.Contains(desc, "my-skill") {
		t.Errorf("FullDescription() should include my-skill, got: %s", desc)
	}
}
