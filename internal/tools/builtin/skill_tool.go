package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/joechenrh/golem/internal/tools"
)

// Ensure SkillTool implements tools.Tool.
var _ tools.Tool = (*SkillTool)(nil)

// SkillTool is a single tool that loads a skill's instructions by name.
// When invoked, it returns the skill body and auto-expands any tools
// mentioned in the skill's markdown via the registry's ExpandHints.
type SkillTool struct {
	store    *tools.SkillStore
	registry *tools.Registry
}

// NewSkillTool creates the skill loader tool.
func NewSkillTool(store *tools.SkillStore, registry *tools.Registry) *SkillTool {
	return &SkillTool{store: store, registry: registry}
}

func (t *SkillTool) Name() string { return "skill" }

func (t *SkillTool) Description() string {
	return "Load a skill's step-by-step instructions by name. " +
		"Call this when you need guidance on how to perform a specific workflow."
}

func (t *SkillTool) FullDescription() string {
	desc := "Load a skill's step-by-step instructions by name. " +
		"Skills are pre-defined workflows that guide you through complex tasks. " +
		"After loading a skill, follow its instructions immediately using other tools.\n\n"

	if t.store != nil {
		summary := t.store.Summary()
		if summary != "" {
			desc += "Available skills:\n" + summary
		}
	}
	return desc
}

var skillToolParams = json.RawMessage(`{
	"type": "object",
	"properties": {
		"name": {
			"type": "string",
			"description": "The skill name to load (e.g. 'summarize-session')."
		}
	},
	"required": ["name"]
}`)

func (t *SkillTool) Parameters() json.RawMessage { return skillToolParams }

func (t *SkillTool) Execute(_ context.Context, args string) (string, error) {
	var params struct {
		Name string `json:"name"`
	}
	if errMsg := tools.ParseArgs(args, &params); errMsg != "" {
		return errMsg, nil
	}
	if params.Name == "" {
		return "Error: name is required", nil
	}

	skill := t.store.Get(params.Name)
	if skill == nil {
		available := t.store.Summary()
		msg := fmt.Sprintf("Error: skill %q not found.", params.Name)
		if available != "" {
			msg += "\n\nAvailable skills:\n" + available
		}
		return msg, nil
	}

	// Auto-expand tools mentioned in the skill body so the LLM
	// has full parameter schemas when acting on the instructions.
	if t.registry != nil {
		t.registry.ExpandHints(skill.Body)
	}

	return skill.Body, nil
}
