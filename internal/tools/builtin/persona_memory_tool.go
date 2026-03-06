package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// PersonaMemoryTool reads and writes the agent's MEMORY.md file.
type PersonaMemoryTool struct {
	memoryPath string
}

// NewPersonaMemoryTool creates a persona_memory tool that operates on the
// given MEMORY.md path. The path must be non-empty.
func NewPersonaMemoryTool(memoryPath string) *PersonaMemoryTool {
	return &PersonaMemoryTool{memoryPath: memoryPath}
}

func (t *PersonaMemoryTool) Name() string { return "persona_memory" }

func (t *PersonaMemoryTool) Description() string {
	return "Read or update the agent's persistent MEMORY.md file."
}

func (t *PersonaMemoryTool) FullDescription() string {
	return "Read or update the agent's persistent MEMORY.md file. " +
		"MEMORY.md is your curated, distilled knowledge — keep it concise (under 200 lines). " +
		"Use action \"read\" to view current contents, \"write\" to replace with new content."
}

func (t *PersonaMemoryTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["read", "write"],
				"description": "Action to perform: read or write."
			},
			"content": {
				"type": "string",
				"description": "New content for MEMORY.md. Required when action is write."
			}
		},
		"required": ["action"]
	}`)
}

func (t *PersonaMemoryTool) Execute(_ context.Context, args string) (string, error) {
	var params struct {
		Action  string `json:"action"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	switch params.Action {
	case "read":
		data, err := os.ReadFile(t.memoryPath)
		if err != nil {
			if os.IsNotExist(err) {
				return "(MEMORY.md does not exist yet)", nil
			}
			return "", fmt.Errorf("reading MEMORY.md: %w", err)
		}
		if len(data) == 0 {
			return "(MEMORY.md is empty)", nil
		}
		return string(data), nil

	case "write":
		if params.Content == "" {
			return "", fmt.Errorf("content is required for write action")
		}
		if err := os.MkdirAll(filepath.Dir(t.memoryPath), 0o755); err != nil {
			return "", fmt.Errorf("creating directory: %w", err)
		}
		if err := os.WriteFile(t.memoryPath, []byte(params.Content), 0o644); err != nil {
			return "", fmt.Errorf("writing MEMORY.md: %w", err)
		}
		return "MEMORY.md updated successfully.", nil

	default:
		return "", fmt.Errorf("unknown action %q: must be \"read\" or \"write\"", params.Action)
	}
}
