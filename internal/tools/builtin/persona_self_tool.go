package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/joechenrh/golem/internal/config"
	"github.com/joechenrh/golem/internal/tools"
)

// Line limits per persona file.
const (
	soulLineLimit   = 100
	agentsLineLimit = 150
	memoryLineLimit = 200
)

// PersonaSelfTool reads and writes the agent's persona files
// (SOUL.md, AGENTS.md, MEMORY.md).
type PersonaSelfTool struct {
	persona *config.Persona
}

// NewPersonaSelfTool creates a persona_self tool that operates on the
// agent's persona files via the given Persona pointer.
func NewPersonaSelfTool(persona *config.Persona) *PersonaSelfTool {
	return &PersonaSelfTool{persona: persona}
}

func (t *PersonaSelfTool) Name() string { return "persona_self" }

func (t *PersonaSelfTool) Description() string {
	return "Read or update the agent's persona files (MEMORY.md, SOUL.md, AGENTS.md)."
}

func (t *PersonaSelfTool) FullDescription() string {
	return "Read or update the agent's persona files. " +
		"MEMORY.md is curated knowledge (under 200 lines). " +
		"SOUL.md is core identity (under 100 lines). " +
		"AGENTS.md is behavioral rules (under 150 lines). " +
		"Use action \"read\" to view current contents, \"write\" to replace with new content. " +
		"SOUL.md and AGENTS.md are backed up to .bak before overwriting."
}

func (t *PersonaSelfTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["read", "write"],
				"description": "Action to perform: read or write."
			},
			"file": {
				"type": "string",
				"enum": ["memory", "soul", "agents"],
				"description": "Which persona file to operate on. Defaults to memory.",
				"default": "memory"
			},
			"content": {
				"type": "string",
				"description": "New content for the file. Required when action is write."
			}
		},
		"required": ["action"]
	}`)
}

// fileSpec holds the resolved path, line limit, getter, setter, and display
// name for a persona file target.
type fileSpec struct {
	path      string
	lineLimit int
	get       func() string
	set       func(string)
	name      string
	backup    bool
}

func (t *PersonaSelfTool) resolveFile(file string) (*fileSpec, error) {
	switch file {
	case "memory", "":
		return &fileSpec{
			path:      t.persona.MemoryPath,
			lineLimit: memoryLineLimit,
			get:       t.persona.GetMemory,
			set:       t.persona.SetMemory,
			name:      "MEMORY.md",
			backup:    false,
		}, nil
	case "soul":
		return &fileSpec{
			path:      t.persona.SoulPath,
			lineLimit: soulLineLimit,
			get:       t.persona.GetSoul,
			set:       t.persona.SetSoul,
			name:      "SOUL.md",
			backup:    true,
		}, nil
	case "agents":
		return &fileSpec{
			path:      t.persona.AgentsPath,
			lineLimit: agentsLineLimit,
			get:       t.persona.GetAgents,
			set:       t.persona.SetAgents,
			name:      "AGENTS.md",
			backup:    true,
		}, nil
	default:
		return nil, fmt.Errorf("unknown file %q: must be \"memory\", \"soul\", or \"agents\"", file)
	}
}

func (t *PersonaSelfTool) Execute(_ context.Context, args string) (string, error) {
	var params struct {
		Action  string `json:"action"`
		File    string `json:"file"`
		Content string `json:"content"`
	}
	if errMsg := tools.ParseArgs(args, &params); errMsg != "" {
		return errMsg, nil
	}

	spec, err := t.resolveFile(params.File)
	if err != nil {
		return "Error: " + err.Error(), nil
	}

	if spec.path == "" {
		return fmt.Sprintf("Error: %s path is not configured", spec.name), nil
	}

	switch params.Action {
	case "read":
		data, err := os.ReadFile(spec.path)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Sprintf("(%s does not exist yet)", spec.name), nil
			}
			return fmt.Sprintf("Error: reading %s: %s", spec.name, err), nil
		}
		if len(data) == 0 {
			return fmt.Sprintf("(%s is empty)", spec.name), nil
		}
		return string(data), nil

	case "write":
		if params.Content == "" {
			return "Error: content is required for write action", nil
		}

		// Enforce line limit.
		lines := strings.Count(params.Content, "\n") + 1
		if lines > spec.lineLimit {
			return fmt.Sprintf("Error: %s exceeds %d line limit (got %d lines)", spec.name, spec.lineLimit, lines), nil
		}

		if err := os.MkdirAll(filepath.Dir(spec.path), 0o755); err != nil {
			return "Error: creating directory: " + err.Error(), nil
		}

		// Backup for soul/agents before overwriting.
		if spec.backup {
			if existing, err := os.ReadFile(spec.path); err == nil && len(existing) > 0 {
				bakPath := spec.path + ".bak"
				if err := os.WriteFile(bakPath, existing, 0o644); err != nil {
					return fmt.Sprintf("Error: creating backup %s: %s", bakPath, err), nil
				}
			}
		}

		if err := os.WriteFile(spec.path, []byte(params.Content), 0o644); err != nil {
			return fmt.Sprintf("Error: writing %s: %s", spec.name, err), nil
		}

		// Update in-memory persona so the next system prompt reflects the change.
		spec.set(params.Content)

		return fmt.Sprintf("%s updated successfully.", spec.name), nil

	default:
		return fmt.Sprintf("Error: unknown action %q: must be \"read\" or \"write\"", params.Action), nil
	}
}
