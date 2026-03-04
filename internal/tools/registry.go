package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/joechenrh/golem/internal/llm"
)

// Registry holds registered tools and manages progressive disclosure state.
type Registry struct {
	tools    map[string]Tool
	expanded map[string]bool
	order    []string // insertion order for deterministic listing
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{
		tools:    make(map[string]Tool),
		expanded: make(map[string]bool),
	}
}

// Register adds a tool to the registry.
func (r *Registry) Register(t Tool) {
	name := t.Name()
	if _, exists := r.tools[name]; !exists {
		r.order = append(r.order, name)
	}
	r.tools[name] = t
}

// RegisterAll adds multiple tools to the registry.
func (r *Registry) RegisterAll(tools ...Tool) {
	for _, t := range tools {
		r.Register(t)
	}
}

// Get returns a tool by name, or nil if not found.
func (r *Registry) Get(name string) Tool {
	return r.tools[name]
}

// Execute runs a tool by name with raw JSON args.
func (r *Registry) Execute(ctx context.Context, name string, args string) (string, error) {
	t, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("unknown tool: %q", name)
	}
	return t.Execute(ctx, args)
}

// ToolDefinitions returns llm.ToolDefinition slice for passing to the LLM.
// Respects progressive disclosure: unexpanded tools get short descriptions.
func (r *Registry) ToolDefinitions() []llm.ToolDefinition {
	defs := make([]llm.ToolDefinition, 0, len(r.tools))
	for _, name := range r.order {
		t := r.tools[name]
		desc := t.Description()
		if r.expanded[name] {
			desc = t.FullDescription()
		}
		defs = append(defs, llm.ToolDefinition{
			Name:        name,
			Description: desc,
			Parameters:  t.Parameters(),
		})
	}
	return defs
}

// Expand marks a tool for full description in subsequent ToolDefinitions() calls.
func (r *Registry) Expand(name string) {
	if _, ok := r.tools[name]; ok {
		r.expanded[name] = true
	}
}

// DetectToolHints scans text for references to registered tool names.
// Returns the names of tools that were mentioned but not yet expanded.
func (r *Registry) DetectToolHints(text string) []string {
	lower := strings.ToLower(text)
	var hints []string
	for name := range r.tools {
		if r.expanded[name] {
			continue
		}
		if strings.Contains(lower, strings.ToLower(name)) {
			hints = append(hints, name)
		}
	}
	sort.Strings(hints)
	return hints
}

// DiscoverSkills scans a directory for SKILL.md files and registers them.
// Pattern: <dir>/*/SKILL.md
func (r *Registry) DiscoverSkills(dir string) error {
	skills, err := discoverSkills(dir)
	if err != nil {
		return err
	}
	for _, s := range skills {
		r.Register(s)
	}
	return nil
}

// List returns a formatted string listing all registered tools.
func (r *Registry) List() string {
	if len(r.order) == 0 {
		return "No tools registered."
	}

	var b strings.Builder
	var builtins, skills []string
	for _, name := range r.order {
		if strings.HasPrefix(name, "skill.") {
			skills = append(skills, name)
		} else {
			builtins = append(builtins, name)
		}
	}

	if len(builtins) > 0 {
		fmt.Fprintf(&b, "Built-in tools (%d):\n", len(builtins))
		for _, name := range builtins {
			fmt.Fprintf(&b, "  %-20s %s\n", name, r.tools[name].Description())
		}
	}
	if len(skills) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "Skills (%d):\n", len(skills))
		for _, name := range skills {
			fmt.Fprintf(&b, "  %-20s %s\n", name, r.tools[name].Description())
		}
	}

	return b.String()
}
