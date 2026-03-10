package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/joechenrh/golem/internal/config"
	"github.com/joechenrh/golem/internal/tools"
)

// Ensure CreateSkillTool implements tools.Tool.
var _ tools.Tool = (*CreateSkillTool)(nil)

var validSkillName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]*$`)

// CreateSkillTool lets an agent create or update skills on disk and register
// them immediately in the skill store.
type CreateSkillTool struct {
	agentName string
	store     *tools.SkillStore
}

// NewCreateSkillTool creates a create_skill tool for the given named agent.
func NewCreateSkillTool(agentName string, store *tools.SkillStore) *CreateSkillTool {
	return &CreateSkillTool{agentName: agentName, store: store}
}

func (t *CreateSkillTool) Name() string { return "create_skill" }

func (t *CreateSkillTool) Description() string {
	return "Create or update a skill on disk and register it immediately."
}

func (t *CreateSkillTool) FullDescription() string {
	return "Create or update a skill on disk and register it immediately in the tool registry. " +
		"The skill is saved as a SKILL.md file with YAML frontmatter (name, description) and a markdown body. " +
		"Skills are saved to the agent's skill directory (~/.golem/agents/<name>/skills/). " +
		"The skill becomes available as skill_<name> right away without restarting."
}

func (t *CreateSkillTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {
				"type": "string",
				"description": "Skill name (alphanumeric and hyphens, must start with alphanumeric)."
			},
			"description": {
				"type": "string",
				"description": "Short description of what the skill does."
			},
			"body": {
				"type": "string",
				"description": "Markdown body with step-by-step instructions for the skill."
			}
		},
		"required": ["name", "description", "body"]
	}`)
}

func (t *CreateSkillTool) Execute(_ context.Context, args string) (string, error) {
	var params struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Body        string `json:"body"`
	}
	if errMsg := tools.ParseArgs(args, &params); errMsg != "" {
		return errMsg, nil
	}

	if t.agentName == "" {
		return "Error: create_skill requires a named agent", nil
	}
	if params.Name == "" {
		return "Error: name is required", nil
	}
	if !validSkillName.MatchString(params.Name) {
		return "Error: name must match [a-zA-Z0-9][a-zA-Z0-9-]* (alphanumeric start, alphanumeric/hyphens)", nil
	}
	if params.Description == "" {
		return "Error: description is required", nil
	}
	if params.Body == "" {
		return "Error: body is required", nil
	}

	dir := filepath.Join(config.GolemHome(), "agents", t.agentName, "skills", params.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Sprintf("Error: creating skill directory: %s", err), nil
	}

	content := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n%s", params.Name, params.Description, params.Body)

	skillPath := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(content), 0o644); err != nil {
		return fmt.Sprintf("Error: writing SKILL.md: %s", err), nil
	}

	t.store.Reload([]string{filepath.Dir(dir)})

	return fmt.Sprintf("Skill %q created and available via the skill tool.", params.Name), nil
}
