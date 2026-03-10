# Skills

Skills are markdown-based prompt documents that guide the agent through complex workflows. Unlike tools, skills don't execute code directly — they provide step-by-step instructions that the agent follows using its available tools.

## Creating a Skill

Create a directory with a `SKILL.md` file:

```
~/.golem/skills/my-skill/SKILL.md
```

The file uses YAML frontmatter followed by markdown instructions:

```markdown
---
name: my-skill
description: Short description of what this skill does
---

## Instructions

Step-by-step instructions for the agent to follow.

1. First, use `read_file` to examine the target file
2. Then, use `edit_file` to make the necessary changes
3. Finally, verify the result with `shell_exec`
```

### Frontmatter Fields

| Field | Required | Description |
|---|---|---|
| `name` | Yes | Unique skill identifier (used to invoke it) |
| `description` | Yes | Short description shown in skill listings |

## Skill Scopes

Skills are discovered from two locations:

1. **Global** — `~/.golem/skills/<skill-name>/SKILL.md` — shared by all agents
2. **Per-agent** — `~/.golem/agents/<name>/skills/<skill-name>/SKILL.md` — agent-specific

Per-agent skills override global ones on name collision.

## Using Skills

### Via the `skill` tool

The agent can load a skill on demand:

```
Load the "summarize-session" skill and follow its instructions.
```

The `skill` tool returns the skill's markdown body and auto-expands any tools mentioned in it, ensuring the agent has full parameter schemas for referenced tools.

### Via `$skill` hints

Reference a skill with `$` prefix in your message to inject it directly into the system prompt:

```
$deploy-checklist Deploy the latest changes to production.
```

This bypasses the LLM round-trip and immediately makes the skill instructions available.

### Via the REPL

List all discovered skills:

```
:skills
```

### Creating skills at runtime

The agent can create new skills during a conversation using the `create_skill` tool. The skill is immediately available for use.

## How Skills Work

1. A compact **skill summary** (name + description for each skill) is included in the system prompt so the agent knows what's available
2. When invoked, the single `skill` tool looks up the skill by name in the `SkillStore` and returns the full markdown body
3. Any tool names mentioned in the skill body are auto-expanded via `Registry.ExpandHints`, giving the agent full parameter schemas
4. The agent follows the instructions using its normal tool-calling capabilities

## Best Practices

- **Be specific** — Include exact tool names and parameters in your instructions
- **Reference tools explicitly** — Mentioning tool names triggers progressive disclosure expansion
- **Keep skills focused** — One skill per workflow; compose complex workflows by referencing other skills
- **Use descriptive names** — The name should hint at the skill's purpose (e.g. `deploy-checklist`, `code-review`)

## Architecture

See [Tool System — Skills](../design/06-tools.md) for the full technical design.
