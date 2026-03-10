package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// mockTool is a simple Tool implementation for testing.
type mockTool struct {
	name        string
	description string
	fullDesc    string
	params      json.RawMessage
	execResult  string
}

func (m *mockTool) Name() string                { return m.name }
func (m *mockTool) Description() string         { return m.description }
func (m *mockTool) FullDescription() string     { return m.fullDesc }
func (m *mockTool) Parameters() json.RawMessage { return m.params }
func (m *mockTool) Execute(_ context.Context, args string) (string, error) {
	return m.execResult, nil
}

func newMockTool(name, desc, fullDesc string) *mockTool {
	return &mockTool{
		name:        name,
		description: desc,
		fullDesc:    fullDesc,
		params:      json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}}}`),
		execResult:  "mock result for " + name,
	}
}

// ─── Registry Tests ──────────────────────────────────────────────

func TestRegistry_RegisterAndExecute(t *testing.T) {
	r := NewRegistry()
	r.Register(newMockTool("read_file", "Read a file", "Read a file with full details"))

	result, err := r.Execute(context.Background(), "read_file", `{"path":"test.txt"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result != "mock result for read_file" {
		t.Errorf("result = %q, want %q", result, "mock result for read_file")
	}
}

func TestRegistry_Middleware(t *testing.T) {
	r := NewRegistry()
	r.Register(newMockTool("test_tool", "desc", "full"))

	var called []string
	r.Use(func(ctx context.Context, name string, args string, next func(context.Context, string) (string, error)) (string, error) {
		called = append(called, "mw1:before:"+name)
		result, err := next(ctx, args)
		called = append(called, "mw1:after")
		return result, err
	})
	r.Use(func(ctx context.Context, name string, args string, next func(context.Context, string) (string, error)) (string, error) {
		called = append(called, "mw2:before:"+name)
		result, err := next(ctx, args)
		called = append(called, "mw2:after")
		return result, err
	})

	result, err := r.Execute(context.Background(), "test_tool", `{}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result != "mock result for test_tool" {
		t.Errorf("result = %q", result)
	}
	// Middlewares should be called in order: mw1 wraps mw2 wraps tool.
	expected := []string{"mw1:before:test_tool", "mw2:before:test_tool", "mw2:after", "mw1:after"}
	if len(called) != len(expected) {
		t.Fatalf("called = %v, want %v", called, expected)
	}
	for i, v := range expected {
		if called[i] != v {
			t.Errorf("called[%d] = %q, want %q", i, called[i], v)
		}
	}
}

func TestRegistry_MiddlewareCanBlock(t *testing.T) {
	r := NewRegistry()
	r.Register(newMockTool("test_tool", "desc", "full"))

	r.Use(func(ctx context.Context, name string, args string, next func(context.Context, string) (string, error)) (string, error) {
		return "blocked", nil // don't call next
	})

	result, err := r.Execute(context.Background(), "test_tool", `{}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result != "blocked" {
		t.Errorf("result = %q, want %q", result, "blocked")
	}
}

func TestRegistry_ExecuteUnknownTool(t *testing.T) {
	r := NewRegistry()
	_, err := r.Execute(context.Background(), "nonexistent", "{}")
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("error = %q, want mention of 'unknown tool'", err.Error())
	}
}

func TestRegistry_RegisterAll(t *testing.T) {
	r := NewRegistry()
	r.RegisterAll(
		newMockTool("tool_a", "desc a", "full a"),
		newMockTool("tool_b", "desc b", "full b"),
	)

	if r.Get("tool_a") == nil {
		t.Error("tool_a not found")
	}
	if r.Get("tool_b") == nil {
		t.Error("tool_b not found")
	}
}

func TestRegistry_Get(t *testing.T) {
	r := NewRegistry()
	r.Register(newMockTool("test_tool", "desc", "full"))

	if r.Get("test_tool") == nil {
		t.Error("Get() returned nil for registered tool")
	}
	if r.Get("nonexistent") != nil {
		t.Error("Get() returned non-nil for unregistered tool")
	}
}

// ─── ToolDefinitions Tests ───────────────────────────────────────

func TestRegistry_ToolDefinitions_ShortDescByDefault(t *testing.T) {
	r := NewRegistry()
	r.Register(newMockTool("read_file", "Read a file", "Read a file with offset and limit support"))

	defs := r.ToolDefinitions()
	if len(defs) != 1 {
		t.Fatalf("len(defs) = %d, want 1", len(defs))
	}
	if defs[0].Description != "Read a file" {
		t.Errorf("Description = %q, want short description", defs[0].Description)
	}
}

func TestRegistry_ToolDefinitions_ExpandedDesc(t *testing.T) {
	r := NewRegistry()
	r.Register(newMockTool("read_file", "Read a file", "Read a file with offset and limit support"))
	r.Expand("read_file")

	defs := r.ToolDefinitions()
	if defs[0].Description != "Read a file with offset and limit support" {
		t.Errorf("Description = %q, want full description after Expand", defs[0].Description)
	}
}

func TestRegistry_ToolDefinitions_PreservesInsertionOrder(t *testing.T) {
	r := NewRegistry()
	r.Register(newMockTool("z_tool", "z", "z"))
	r.Register(newMockTool("a_tool", "a", "a"))
	r.Register(newMockTool("m_tool", "m", "m"))

	defs := r.ToolDefinitions()
	if len(defs) != 3 {
		t.Fatalf("len(defs) = %d, want 3", len(defs))
	}
	if defs[0].Name != "z_tool" || defs[1].Name != "a_tool" || defs[2].Name != "m_tool" {
		t.Errorf("order = [%s, %s, %s], want [z_tool, a_tool, m_tool]",
			defs[0].Name, defs[1].Name, defs[2].Name)
	}
}

func TestRegistry_ToolDefinitions_CompactParamsWhenUnexpanded(t *testing.T) {
	r := NewRegistry()
	r.Register(newMockTool("test_tool", "desc", "full"))

	defs := r.ToolDefinitions()
	// Unexpanded tools should get compact params.
	var parsed map[string]any
	if err := json.Unmarshal(defs[0].Parameters, &parsed); err != nil {
		t.Fatalf("Parameters is not valid JSON: %v", err)
	}
	if parsed["type"] != "object" {
		t.Errorf("Parameters.type = %v, want %q", parsed["type"], "object")
	}
	props, hasProps := parsed["properties"]
	if !hasProps {
		t.Error("compact params should include properties key for API compatibility")
	}
	if m, ok := props.(map[string]any); !ok || len(m) != 0 {
		t.Error("compact params properties should be an empty object")
	}
}

func TestRegistry_ToolDefinitions_FullParamsWhenExpanded(t *testing.T) {
	r := NewRegistry()
	r.Register(newMockTool("test_tool", "desc", "full"))
	r.Expand("test_tool")

	defs := r.ToolDefinitions()
	var parsed map[string]any
	if err := json.Unmarshal(defs[0].Parameters, &parsed); err != nil {
		t.Fatalf("Parameters is not valid JSON: %v", err)
	}
	if _, hasProps := parsed["properties"]; !hasProps {
		t.Error("expanded tool should include full properties in params")
	}
}

// ─── Progressive Disclosure Tests ────────────────────────────────

func TestRegistry_Expand_UnknownTool(t *testing.T) {
	r := NewRegistry()
	// Should not panic.
	r.Expand("nonexistent")
}

func TestRegistry_DetectToolHints(t *testing.T) {
	r := NewRegistry()
	r.Register(newMockTool("read_file", "Read", "Read full"))
	r.Register(newMockTool("write_file", "Write", "Write full"))

	hints := r.DetectToolHints("I'll use read_file to check the contents")
	if len(hints) != 1 || hints[0] != "read_file" {
		t.Errorf("hints = %v, want [read_file]", hints)
	}
}

func TestRegistry_DetectToolHints_CaseInsensitive(t *testing.T) {
	r := NewRegistry()
	r.Register(newMockTool("shell_exec", "Run shell", "Run shell full"))

	hints := r.DetectToolHints("Let me use Shell_Exec to run that")
	if len(hints) != 1 || hints[0] != "shell_exec" {
		t.Errorf("hints = %v, want [shell_exec]", hints)
	}
}

func TestRegistry_DetectToolHints_SkipsExpanded(t *testing.T) {
	r := NewRegistry()
	r.Register(newMockTool("read_file", "Read", "Read full"))
	r.Expand("read_file")

	hints := r.DetectToolHints("I'll use read_file")
	if len(hints) != 0 {
		t.Errorf("hints = %v, want empty (already expanded)", hints)
	}
}

func TestRegistry_ExpandHints(t *testing.T) {
	r := NewRegistry()
	r.Register(newMockTool("read_file", "Read", "Read full"))
	r.Register(newMockTool("write_file", "Write", "Write full"))

	expanded := r.ExpandHints("I need to read_file first")
	if len(expanded) != 1 || expanded[0] != "read_file" {
		t.Errorf("expanded = %v, want [read_file]", expanded)
	}

	// Verify read_file is now expanded.
	defs := r.ToolDefinitions()
	for _, d := range defs {
		if d.Name == "read_file" && d.Description != "Read full" {
			t.Error("read_file should have full description after ExpandHints")
		}
		if d.Name == "write_file" && d.Description != "Write" {
			t.Error("write_file should still have short description")
		}
	}
}

func TestRegistry_ShrinkUnused(t *testing.T) {
	r := NewRegistry()
	r.Register(newMockTool("read_file", "Read", "Read full"))
	r.Register(newMockTool("write_file", "Write", "Write full"))
	r.Register(newMockTool("shell_exec", "Shell", "Shell full"))

	// Expand read_file at iter 0, write_file at iter 1.
	r.ExpandAt("read_file", 0)
	r.ExpandAt("write_file", 1)
	// Expand shell_exec without iteration (e.g. pre-expanded by config).
	r.Expand("shell_exec")

	// At iter 5 with staleAfter=3: read_file (last=0, gap=5) should shrink,
	// write_file (last=1, gap=4) should shrink, shell_exec should stay.
	r.ShrinkUnused(5, 3)

	defs := r.ToolDefinitions()
	for _, d := range defs {
		switch d.Name {
		case "read_file":
			if d.Description != "Read" {
				t.Errorf("read_file should be shrunk back to short desc, got %q", d.Description)
			}
		case "write_file":
			if d.Description != "Write" {
				t.Errorf("write_file should be shrunk back to short desc, got %q", d.Description)
			}
		case "shell_exec":
			if d.Description != "Shell full" {
				t.Errorf("shell_exec should stay expanded (no iter tracking), got %q", d.Description)
			}
		}
	}
}

// ─── Skill Tests ─────────────────────────────────────────────────

func TestParseSkillFile(t *testing.T) {
	path := filepath.Join("testdata", "skills", "my-skill", "SKILL.md")
	meta, err := parseSkillFile(path)
	if err != nil {
		t.Fatalf("parseSkillFile() error: %v", err)
	}

	if meta.Name != "my-skill" {
		t.Errorf("Name = %q, want %q", meta.Name, "my-skill")
	}
	if meta.Description == "" {
		t.Error("Description should not be empty")
	}
	if !strings.Contains(meta.Body, "My Skill Instructions") {
		t.Error("Body should contain skill content")
	}
}

func TestParseSkillFile_MissingFile(t *testing.T) {
	_, err := parseSkillFile("nonexistent/SKILL.md")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestParseFrontmatter_MissingDelimiter(t *testing.T) {
	_, _, _, err := parseFrontmatter("no frontmatter here")
	if err == nil {
		t.Fatal("expected error for missing frontmatter")
	}
}

func TestParseFrontmatter_MissingName(t *testing.T) {
	content := "---\ndescription: test\n---\nbody"
	_, _, _, err := parseFrontmatter(content)
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestParseFrontmatter_MissingDescription(t *testing.T) {
	content := "---\nname: test\n---\nbody"
	_, _, _, err := parseFrontmatter(content)
	if err == nil {
		t.Fatal("expected error for missing description")
	}
}

// ─── SkillStore Tests ────────────────────────────────────────────

func TestSkillStore_DiscoverAndGet(t *testing.T) {
	store := NewSkillStore()
	err := store.Discover(filepath.Join("testdata", "skills"))
	if err != nil {
		t.Fatalf("Discover() error: %v", err)
	}

	skill := store.Get("my-skill")
	if skill == nil {
		t.Fatal("expected to find my-skill")
	}
	if skill.Name != "my-skill" {
		t.Errorf("Name = %q, want %q", skill.Name, "my-skill")
	}
	if skill.Description == "" {
		t.Error("Description should not be empty")
	}
	if !strings.Contains(skill.Body, "My Skill Instructions") {
		t.Error("Body should contain skill content")
	}
}

func TestSkillStore_List(t *testing.T) {
	store := NewSkillStore()
	store.Discover(filepath.Join("testdata", "skills"))

	skills := store.List()
	if len(skills) != 1 {
		t.Fatalf("List() returned %d skills, want 1", len(skills))
	}
	if skills[0].Name != "my-skill" {
		t.Errorf("skills[0].Name = %q, want %q", skills[0].Name, "my-skill")
	}
}

func TestSkillStore_Get_NotFound(t *testing.T) {
	store := NewSkillStore()
	if store.Get("nonexistent") != nil {
		t.Error("expected nil for nonexistent skill")
	}
}

func TestSkillStore_Discover_MissingDir(t *testing.T) {
	store := NewSkillStore()
	err := store.Discover("nonexistent-dir")
	if err != nil {
		t.Fatalf("Discover() should silently handle missing dir, got: %v", err)
	}
}

// ─── List Tests ──────────────────────────────────────────────────

func TestRegistry_List(t *testing.T) {
	r := NewRegistry()
	r.Register(newMockTool("read_file", "Read a file", "Read full"))
	r.Register(newMockTool("write_file", "Write a file", "Write full"))

	list := r.List()
	if !strings.Contains(list, "Built-in tools (2)") {
		t.Errorf("List() missing header: %s", list)
	}
	if !strings.Contains(list, "read_file") || !strings.Contains(list, "write_file") {
		t.Errorf("List() missing tool names: %s", list)
	}
}

func TestRegistry_List_WithSkillStore(t *testing.T) {
	r := NewRegistry()
	r.Register(newMockTool("read_file", "Read a file", "Read full"))

	store := NewSkillStore()
	store.Discover(filepath.Join("testdata", "skills"))
	r.SetSkillStore(store)

	list := r.List()
	if !strings.Contains(list, "Built-in tools (1)") {
		t.Errorf("List() missing builtin header: %s", list)
	}
	if !strings.Contains(list, "Skills (1)") {
		t.Errorf("List() missing skills header: %s", list)
	}
	if !strings.Contains(list, "my-skill") {
		t.Errorf("List() missing skill name: %s", list)
	}
}

func TestRegistry_List_Empty(t *testing.T) {
	r := NewRegistry()
	list := r.List()
	if list != "No tools registered." {
		t.Errorf("List() = %q, want %q", list, "No tools registered.")
	}
}
