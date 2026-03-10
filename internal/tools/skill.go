package tools

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SkillMetadata holds a parsed skill's data without implementing Tool.
type SkillMetadata struct {
	Name        string // skill name from frontmatter (e.g. "summarize-session")
	Description string // short description from frontmatter
	Body        string // markdown body (full instructions)
}

// SkillStore discovers and holds skills separately from the tool registry.
type SkillStore struct {
	skills map[string]*SkillMetadata
	order  []string // insertion order for deterministic listing
}

// NewSkillStore creates an empty skill store.
func NewSkillStore() *SkillStore {
	return &SkillStore{
		skills: make(map[string]*SkillMetadata),
	}
}

// Get returns a skill by name, or nil if not found.
func (s *SkillStore) Get(name string) *SkillMetadata {
	return s.skills[name]
}

// List returns all skills in discovery order.
func (s *SkillStore) List() []*SkillMetadata {
	result := make([]*SkillMetadata, 0, len(s.order))
	for _, name := range s.order {
		result = append(result, s.skills[name])
	}
	return result
}

// Discover scans dir for subdirectories containing SKILL.md files.
// Pattern: <dir>/*/SKILL.md
func (s *SkillStore) Discover(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read skills dir %s: %w", dir, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillPath := filepath.Join(dir, entry.Name(), "SKILL.md")
		if _, err := os.Stat(skillPath); os.IsNotExist(err) {
			continue
		}
		meta, err := parseSkillFile(skillPath)
		if err != nil {
			log.Printf("skipping invalid skill %s: %v", skillPath, err)
			continue
		}
		if _, exists := s.skills[meta.Name]; !exists {
			s.order = append(s.order, meta.Name)
		}
		s.skills[meta.Name] = meta
	}

	return nil
}

// Reload re-discovers skills from the given directories. Returns the count
// of added/updated skills. Deleted skills are NOT removed to avoid breaking
// mid-conversation references.
func (s *SkillStore) Reload(dirs []string) int {
	updated := 0
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			skillPath := filepath.Join(dir, entry.Name(), "SKILL.md")
			if _, err := os.Stat(skillPath); os.IsNotExist(err) {
				continue
			}
			meta, err := parseSkillFile(skillPath)
			if err != nil {
				continue
			}
			existing := s.skills[meta.Name]
			if existing != nil && existing.Body == meta.Body {
				continue
			}
			if _, exists := s.skills[meta.Name]; !exists {
				s.order = append(s.order, meta.Name)
			}
			s.skills[meta.Name] = meta
			updated++
		}
	}
	return updated
}

// Summary returns a compact multi-line string listing all skills
// (name + description), suitable for injection into the system prompt.
func (s *SkillStore) Summary() string {
	if len(s.order) == 0 {
		return ""
	}
	var b strings.Builder
	for _, name := range s.order {
		meta := s.skills[name]
		fmt.Fprintf(&b, "- %s: %s\n", meta.Name, meta.Description)
	}
	return b.String()
}

// skillHintRe matches $skill-name references in user text.
var skillHintRe = regexp.MustCompile(`\$([A-Za-z0-9_.-]+)`)

// ExpandSkillHints scans text for $skillname patterns and returns
// the matching SkillMetadata objects. Does not modify the text.
func (s *SkillStore) ExpandSkillHints(text string) []*SkillMetadata {
	matches := skillHintRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}

	// Deduplicate matches while preserving order.
	seen := make(map[string]bool)
	var result []*SkillMetadata
	for _, m := range matches {
		name := m[1]
		if seen[name] {
			continue
		}
		seen[name] = true
		if skill := s.skills[name]; skill != nil {
			result = append(result, skill)
		}
	}
	return result
}

// parseSkillFile reads a SKILL.md file and returns SkillMetadata.
func parseSkillFile(path string) (*SkillMetadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read skill %s: %w", path, err)
	}

	name, description, body, err := parseFrontmatter(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse skill %s: %w", path, err)
	}

	return &SkillMetadata{
		Name:        name,
		Description: description,
		Body:        body,
	}, nil
}

// parseFrontmatter extracts name, description, and body from a SKILL.md file.
// Expects YAML-like frontmatter delimited by "---" lines.
func parseFrontmatter(
	content string,
) (name, description, body string, err error) {
	content = strings.TrimLeft(content, " \t\r\n")

	if !strings.HasPrefix(content, "---") {
		return "", "", "", fmt.Errorf("missing frontmatter delimiter '---'")
	}

	rest := content[3:]
	rest = strings.TrimLeft(rest, " \t\r")
	if len(rest) > 0 && rest[0] == '\n' {
		rest = rest[1:]
	}

	endIdx := strings.Index(rest, "\n---")
	if endIdx == -1 {
		return "", "", "", fmt.Errorf("missing closing frontmatter delimiter '---'")
	}

	frontmatter := rest[:endIdx]
	body = strings.TrimLeft(rest[endIdx+4:], " \t\r\n")

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
