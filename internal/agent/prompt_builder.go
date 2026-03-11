package agent

import (
	"fmt"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/joechenrh/golem/internal/config"
	"github.com/joechenrh/golem/internal/tools"
)

// PromptBuilder constructs and caches the system prompt for LLM calls.
// It handles persona-based and flat prompt assembly, skill reload, and
// skill hint expansion.
type PromptBuilder struct {
	config              *config.Config
	tools               *tools.Registry
	logger              *zap.Logger
	skillDirs           []string
	skillReloadInterval time.Duration
	lastSkillReload     time.Time
	cachedPrompt        string
}

// Build constructs the system prompt from config and persona state.
func (pb *PromptBuilder) Build() string {
	if pb.config.Persona.HasPersona() {
		return pb.buildPersonaPrompt()
	}
	return pb.buildFlatPrompt()
}

// CachedPrompt returns the currently cached system prompt.
func (pb *PromptBuilder) CachedPrompt() string {
	return pb.cachedPrompt
}

// SetCachedPrompt stores the cached system prompt.
func (pb *PromptBuilder) SetCachedPrompt(prompt string) {
	pb.cachedPrompt = prompt
}

// MaybeReloadSkills re-discovers skills from disk if enough time has elapsed.
func (pb *PromptBuilder) MaybeReloadSkills() {
	if pb.skillReloadInterval <= 0 || len(pb.skillDirs) == 0 {
		return
	}
	if time.Since(pb.lastSkillReload) < pb.skillReloadInterval {
		return
	}
	pb.lastSkillReload = time.Now()
	if store := pb.tools.GetSkillStore(); store != nil {
		if n := store.Reload(pb.skillDirs); n > 0 {
			pb.logger.Info("reloaded skills from disk", zap.Int("updated", n))
		}
	}
}

// ExpandSkillHints scans text for $skill-name references, appends matched
// skill bodies to the cached system prompt, and auto-expands any tools
// mentioned in the skill body via ExpandHints.
func (pb *PromptBuilder) ExpandSkillHints(text string) {
	store := pb.tools.GetSkillStore()
	if store == nil {
		return
	}
	skills := store.ExpandSkillHints(text)
	if len(skills) == 0 {
		return
	}
	var b strings.Builder
	b.WriteString(pb.cachedPrompt)
	for _, skill := range skills {
		b.WriteString("\n## Skill: ")
		b.WriteString(skill.Name)
		b.WriteString("\n\n")
		b.WriteString(skill.Body)
		b.WriteByte('\n')

		// Auto-expand any tools referenced in the skill body.
		pb.tools.ExpandHints(skill.Body)
	}
	pb.cachedPrompt = b.String()
	pb.logger.Debug("expanded skill hints into system prompt",
		zap.Int("skill_count", len(skills)))
}

// buildPersonaPrompt assembles the three-layer persona system prompt.
func (pb *PromptBuilder) buildPersonaPrompt() string {
	p := pb.config.Persona
	var b strings.Builder

	soul := p.GetSoul()
	agents := p.GetAgents()
	memory := p.GetMemory()

	// --- Layer 1: Identity ---
	b.WriteString("# Identity\n\n")
	b.WriteString(soul)
	b.WriteByte('\n')
	if p.User != "" {
		b.WriteString("\n## User Profile\n\n")
		b.WriteString(p.User)
		b.WriteByte('\n')
	}

	// --- Layer 2: Operations ---
	b.WriteString("\n# Operations\n\n")
	if agents != "" {
		b.WriteString(agents)
		b.WriteByte('\n')
	}
	b.WriteString("\n## Tool Use\n\n")
	b.WriteString(toolUseInstruction)

	// Skill summary — let the LLM know what skills are available.
	if skillStore := pb.tools.GetSkillStore(); skillStore != nil {
		if summary := skillStore.Summary(); summary != "" {
			b.WriteString("\n## Available Skills\n\n")
			b.WriteString("Use the `skill` tool to load detailed instructions for any of these workflows:\n")
			b.WriteString(summary)
		}
	}

	// --- Layer 3: Knowledge ---
	b.WriteString("\n# Knowledge\n\n")
	b.WriteString("Use the persona_self tool to read/update your persona files: ")
	b.WriteString("SOUL.md (identity), AGENTS.md (rules), MEMORY.md (knowledge & preferences). ")
	b.WriteString("Update MEMORY.md regularly for learned patterns and user preferences.\n")

	if memory != "" {
		b.WriteString("\n## Current Memory\n\n")
		b.WriteString(memory)
		b.WriteByte('\n')
	}

	// --- Environment ---
	b.WriteString("\n# Environment\n\n")
	fmt.Fprintf(&b, "Working directory: %s\n", pb.config.WorkspaceDir)
	fmt.Fprintf(&b, "Current time: %s\n", time.Now().Format(time.RFC3339))

	return b.String()
}

// buildFlatPrompt is the legacy system prompt assembly (no persona files).
func (pb *PromptBuilder) buildFlatPrompt() string {
	var b strings.Builder

	b.WriteString("You are golem, a helpful coding assistant.\n\n")

	fmt.Fprintf(&b, "Working directory: %s\n", pb.config.WorkspaceDir)
	fmt.Fprintf(&b, "Current time: %s\n\n", time.Now().Format(time.RFC3339))

	b.WriteString(toolUseInstruction)
	b.WriteByte('\n')

	// Skill summary — let the LLM know what skills are available.
	if skillStore := pb.tools.GetSkillStore(); skillStore != nil {
		if summary := skillStore.Summary(); summary != "" {
			b.WriteString("## Available Skills\n\n")
			b.WriteString("Use the `skill` tool to load detailed instructions for any of these workflows:\n")
			b.WriteString(summary)
			b.WriteByte('\n')
		}
	}

	switch {
	case pb.config.SystemPrompt != "":
		b.WriteString(pb.config.SystemPrompt)
		b.WriteByte('\n')
	default:
		if data, err := os.ReadFile(".agent/system-prompt.md"); err == nil {
			b.WriteString(strings.TrimSpace(string(data)))
			b.WriteByte('\n')
		}
	}

	return b.String()
}
