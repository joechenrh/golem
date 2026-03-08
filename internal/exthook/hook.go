// Package exthook provides external lifecycle hooks for golem agents.
// External hooks are separate from internal hooks (hooks.Bus): they run
// user-defined shell commands at lifecycle points and exchange data via
// a JSON protocol on stdin/stdout.
package exthook

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EventType for external hooks (intentionally separate from hooks.EventType).
type EventType string

const (
	EventBeforeLLMCall EventType = "before_llm_call"
	EventAfterLLMCall  EventType = "after_llm_call"
	EventAfterReset    EventType = "after_reset"
	EventUserMessage   EventType = "user_message"
)

var validEvents = map[EventType]bool{
	EventBeforeLLMCall: true,
	EventAfterLLMCall:  true,
	EventAfterReset:    true,
	EventUserMessage:   true,
}

// IsBlocking returns true for events that can inject content (before_* events).
func (e EventType) IsBlocking() bool {
	return strings.HasPrefix(string(e), "before_")
}

// HookDef represents a parsed HOOK.md definition.
type HookDef struct {
	Name        string
	Description string
	Events      []EventType
	Command     string        // absolute path to executable
	Dir         string        // working directory (hook's parent dir)
	Timeout     time.Duration // default 10s
}

// ParseHook reads a HOOK.md file and returns a HookDef.
func ParseHook(path string) (*HookDef, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read hook %s: %w", path, err)
	}

	content := string(data)
	name, description, _, err := parseFrontmatterFields(content)
	if err != nil {
		return nil, fmt.Errorf("parse hook %s: %w", path, err)
	}

	fm := extractFrontmatter(content)

	events, err := parseEvents(fm)
	if err != nil {
		return nil, fmt.Errorf("parse hook %s: %w", path, err)
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("parse hook %s: no valid events specified", path)
	}

	command := parseFMField(fm, "command")
	if command == "" {
		return nil, fmt.Errorf("parse hook %s: missing 'command' field", path)
	}

	hookDir, _ := filepath.Abs(filepath.Dir(path))
	// Resolve relative command paths against the hook directory.
	if !filepath.IsAbs(command) {
		command = filepath.Join(hookDir, command)
	}

	timeout := 10 * time.Second
	if ts := parseFMField(fm, "timeout"); ts != "" {
		if d, err := time.ParseDuration(ts); err == nil {
			timeout = d
		}
	}

	return &HookDef{
		Name:        name,
		Description: description,
		Events:      events,
		Command:     command,
		Dir:         hookDir,
		Timeout:     timeout,
	}, nil
}

// extractFrontmatter returns the raw frontmatter section between --- delimiters.
func extractFrontmatter(content string) string {
	content = strings.TrimLeft(content, " \t\r\n")
	if !strings.HasPrefix(content, "---") {
		return ""
	}
	rest := content[3:]
	rest = strings.TrimLeft(rest, " \t\r")
	if len(rest) > 0 && rest[0] == '\n' {
		rest = rest[1:]
	}
	endIdx := strings.Index(rest, "\n---")
	if endIdx == -1 {
		return ""
	}
	return rest[:endIdx]
}

// parseFrontmatterFields extracts name and description from frontmatter.
func parseFrontmatterFields(content string) (name, description, body string, err error) {
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
	fm := rest[:endIdx]
	body = strings.TrimLeft(rest[endIdx+4:], " \t\r\n")

	name = parseFMField(fm, "name")
	description = parseFMField(fm, "description")
	if name == "" {
		return "", "", "", fmt.Errorf("frontmatter missing 'name' field")
	}
	if description == "" {
		return "", "", "", fmt.Errorf("frontmatter missing 'description' field")
	}
	return name, description, body, nil
}

// parseFMField extracts a simple key: value field from frontmatter text.
func parseFMField(fm, key string) string {
	for _, line := range strings.Split(fm, "\n") {
		line = strings.TrimSpace(line)
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// parseEvents extracts the events list from frontmatter.
// Supports both inline YAML list (events: [a, b]) and multi-line (- a).
func parseEvents(fm string) ([]EventType, error) {
	var events []EventType
	lines := strings.Split(fm, "\n")
	inEvents := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "events:") {
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "events:"))
			if rest != "" {
				// Inline format: events: [before_llm_call, after_reset]
				rest = strings.Trim(rest, "[]")
				for _, part := range strings.Split(rest, ",") {
					part = strings.TrimSpace(part)
					et := EventType(part)
					if validEvents[et] {
						events = append(events, et)
					}
				}
				inEvents = false
			} else {
				inEvents = true
			}
			continue
		}

		if inEvents {
			if strings.HasPrefix(trimmed, "- ") {
				val := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
				et := EventType(val)
				if validEvents[et] {
					events = append(events, et)
				}
			} else if trimmed != "" {
				// No longer in the events list.
				inEvents = false
			}
		}
	}
	return events, nil
}
