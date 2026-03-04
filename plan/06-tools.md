# Step 6: Tool Framework

## Scope

Tool interface, registry with discovery, SKILL.md parsing, and progressive disclosure. Maps to crabclaw's `tools/registry.rs` + `tools/progressive.rs`.

## Files

- `internal/tools/tool.go` — Tool interface
- `internal/tools/registry.go` — Registry with registration, execution, discovery
- `internal/tools/skill.go` — SKILL.md parser
- `internal/tools/progressive.go` — Progressive disclosure logic

## Key Points

### Tool Interface (`tool.go`)

```go
type Tool interface {
    Name() string
    Description() string                              // short (for compact/progressive mode)
    FullDescription() string                           // full (for expanded mode), defaults to Description()
    Parameters() map[string]interface{}                // JSON Schema
    Execute(ctx context.Context, args string) (string, error)  // args is raw JSON
}
```

Tools receive raw JSON arguments and return a string result. The tool is responsible for parsing its own arguments from the JSON string.

### Registry (`registry.go`)

```go
type Registry struct {
    tools    map[string]Tool      // name -> tool
    expanded map[string]bool      // which tools are in expanded mode
}

func NewRegistry() *Registry
func (r *Registry) Register(t Tool)
func (r *Registry) RegisterAll(tools ...Tool)

// Execute runs a tool by name with JSON args. Returns result string or error.
func (r *Registry) Execute(ctx context.Context, name string, args string) (string, error)

// ToolDefinitions returns llm.ToolDefinition slice for passing to LLM.
// Respects progressive disclosure — unexpanded tools get short descriptions.
func (r *Registry) ToolDefinitions() []llm.ToolDefinition

// Expand marks a tool for full description in next ToolDefinitions() call.
func (r *Registry) Expand(name string)

// DiscoverSkills scans a directory for SKILL.md files and registers them.
// Path pattern: <dir>/*/SKILL.md
func (r *Registry) DiscoverSkills(dir string) error

// List returns a formatted string listing all registered tools (for ,tools command).
func (r *Registry) List() string
```

### Skill Parser (`skill.go`)

Parses SKILL.md files with YAML frontmatter:

```markdown
---
name: my-skill
description: Short description for tool listing
---
# Full Skill Instructions

Detailed instructions that the agent receives when this skill is invoked...
```

```go
type SkillMeta struct {
    Name        string `yaml:"name"`
    Description string `yaml:"description"`
}

// ParseSkill reads a SKILL.md file and returns a Tool implementation.
func ParseSkill(path string) (Tool, error)
```

The returned tool:
- `Name()` returns `"skill.<name>"` (e.g., `skill.my-skill`)
- `Description()` returns the YAML description
- `FullDescription()` returns the markdown body
- `Parameters()` returns `{"type": "object", "properties": {"input": {"type": "string"}}}` — skills take a single text input
- `Execute()` returns the skill body as context for the LLM (skills are prompt injections, not executable code)

### Progressive Disclosure (`progressive.go`)

Follows crabclaw's approach to save tokens:

1. **First turn**: All tools sent with short `Description()` only (~50 tokens each)
2. **When model references a tool**: That tool gets `Expand(name)` called
3. **Subsequent turns**: Expanded tools include `FullDescription()` and full parameter schema

Detection: After each LLM response, scan for tool name mentions. If the model says something like "I'll use read_file" but doesn't make a formal tool call, expand that tool for the next turn.

```go
// DetectToolHints scans text for references to registered tool names.
// Returns tool names that should be expanded.
func (r *Registry) DetectToolHints(text string) []string
```

### Tool Execution Flow

```
LLM returns ToolCall{Name: "read_file", Arguments: '{"path": "/tmp/x.txt"}'}
    │
    ├─ Registry.Execute("read_file", '{"path": "/tmp/x.txt"}')
    │   ├─ Look up tool by name
    │   ├─ Call tool.Execute(ctx, args)
    │   └─ Return result string (file contents) or error string
    │
    └─ Result is added as a tool result message for the next LLM call
```

## Design Decisions

- Tools parse their own JSON args (no generic arg parsing in registry) — keeps each tool self-contained
- Skills are "prompt tools" not "executable tools" — they inject instructions, the LLM decides what to do
- Progressive disclosure is simple name-matching, not ML-based — follows crabclaw's approach
- Tool names are lowercase with underscores (e.g., `read_file`, `shell_exec`)
- Skill names are prefixed with `skill.` to avoid collision with built-in tools

## Done When

- `Registry.Register(tool)` + `Registry.Execute(name, args)` works
- `ToolDefinitions()` returns valid `[]llm.ToolDefinition`
- `DiscoverSkills("testdata/skills")` finds and registers SKILL.md files
- `DetectToolHints("I'll use read_file")` returns `["read_file"]`
- `Expand("read_file")` switches that tool to full description mode
