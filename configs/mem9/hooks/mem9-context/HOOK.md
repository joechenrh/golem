---
name: mem9-context
description: Inject relevant memories before LLM calls and save session summaries
events: [before_llm_call, after_reset]
command: ./handler.py
timeout: 15s
---

# mem9 Context Hook

Queries mem9 for memories relevant to the current user message
and injects them as context before LLM calls. On session reset,
saves the session summary to mem9 for future recall.
