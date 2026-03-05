package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// skillParams is the fixed JSON Schema for skill tools — a single text input.
var skillParams = json.RawMessage(`{"type":"object","properties":{"input":{"type":"string","description":"Input text for the skill"}},"required":["input"]}`)

// skillTool wraps a parsed SKILL.md as a Tool.
type skillTool struct {
	name        string // "skill_<name>"
	description string // from frontmatter
	body        string // markdown body (full instructions)
}

func (s *skillTool) Name() string                { return s.name }
func (s *skillTool) Description() string         { return s.description }
func (s *skillTool) FullDescription() string     { return s.body }
func (s *skillTool) Parameters() json.RawMessage { return skillParams }

// Execute returns the skill body as context for the LLM.
// Skills are prompt injections, not executable code.
func (s *skillTool) Execute(
	_ context.Context, _ string,
) (string, error) {
	return s.body, nil
}

// ParseSkill reads a SKILL.md file and returns a Tool implementation.
// The file must have YAML-like frontmatter delimited by "---" lines
// containing at least "name:" and "description:" fields.
func ParseSkill(path string) (Tool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read skill %s: %w", path, err)
	}

	content := string(data)
	name, description, body, err := parseFrontmatter(content)
	if err != nil {
		return nil, fmt.Errorf("parse skill %s: %w", path, err)
	}

	return &skillTool{
		name:        "skill_" + name,
		description: description,
		body:        body,
	}, nil
}

// parseFrontmatter extracts name, description, and body from a SKILL.md file.
// Expects YAML-like frontmatter delimited by "---" lines.
func parseFrontmatter(
	content string,
) (name, description, body string, err error) {
	// Trim leading whitespace/newlines.
	content = strings.TrimLeft(content, " \t\r\n")

	if !strings.HasPrefix(content, "---") {
		return "", "", "", fmt.Errorf("missing frontmatter delimiter '---'")
	}

	// Find the closing "---".
	rest := content[3:] // skip opening "---"
	rest = strings.TrimLeft(rest, " \t\r")
	if len(rest) > 0 && rest[0] == '\n' {
		rest = rest[1:]
	}

	endIdx := strings.Index(rest, "\n---")
	if endIdx == -1 {
		return "", "", "", fmt.Errorf("missing closing frontmatter delimiter '---'")
	}

	frontmatter := rest[:endIdx]
	body = strings.TrimLeft(rest[endIdx+4:], " \t\r\n") // skip "\n---"

	// Parse simple YAML key-value pairs.
	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "name":
			name = value
		case "description":
			description = value
		}
	}

	if name == "" {
		return "", "", "", fmt.Errorf("frontmatter missing 'name' field")
	}
	if description == "" {
		return "", "", "", fmt.Errorf("frontmatter missing 'description' field")
	}

	return name, description, body, nil
}

// discoverSkills scans dir for subdirectories containing SKILL.md files.
// Pattern: <dir>/*/SKILL.md
func discoverSkills(dir string) ([]Tool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no skills directory is fine
		}
		return nil, fmt.Errorf("read skills dir %s: %w", dir, err)
	}

	var skills []Tool
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillPath := filepath.Join(dir, entry.Name(), "SKILL.md")
		if _, err := os.Stat(skillPath); os.IsNotExist(err) {
			continue
		}
		t, err := ParseSkill(skillPath)
		if err != nil {
			// Skip invalid skills with a note, don't fail the whole discovery.
			continue
		}
		skills = append(skills, t)
	}

	return skills, nil
}
