---
name: summarize_session
description: Summarize the current session's work and optionally save to memory
---

# Session Summary Skill

When the user asks to summarize the session (or current conversation), follow this workflow:

## Step 1: Gather Session Info

Run the `:tape.info` command by outputting it on its own line to get tape statistics (total entries, anchors, entries since last anchor).

## Step 2: Analyze the Conversation

Review what happened in this session by considering:
- **Tasks completed**: What did the user ask for? What was accomplished?
- **Tools used**: Which tools were called and how often? (shell commands, file edits, web searches, etc.)
- **Key decisions**: Any architectural choices, trade-offs, or design decisions made
- **Files changed**: Which files were created, modified, or deleted
- **Unfinished work**: Anything that was started but not completed, or explicitly deferred

## Step 3: Write the Summary

Produce a structured summary in this format:

```
**Session Summary**

**Tasks**
- [Completed] Brief description of each task
- [In Progress] Any unfinished work
- [Deferred] Anything explicitly postponed

**Key Changes**
- List of files/components that were modified and why

**Decisions**
- Any notable decisions or trade-offs (if applicable)

**Open Items**
- Things to follow up on in a future session (if any)
```

## Step 4: Save to Memory (if requested)

If the user asks to save the summary, or if this was a significant work session:
1. Use `persona_self` (action: "read") to check current memory contents
2. Use `persona_self` (action: "write") to append or update the summary
3. Keep memory concise — store only the essential takeaways, not the full summary

If the mnemos memory system is available, also consider using `memory_store` to save key decisions or facts for cross-session recall.

## Guidelines

- Be concise — the summary should be scannable in 30 seconds
- Focus on **what changed** and **why**, not play-by-play of tool calls
- Group related changes together rather than listing chronologically
- If the session was short or trivial (e.g., a single question), keep the summary to 2-3 lines
- Use the user's language (Chinese if they spoke Chinese, English otherwise)
