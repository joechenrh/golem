---
name: mem9-recall
description: Inject relevant memories before LLM calls
events: [before_llm_call]
command: ./handler.py
timeout: 15s
---

# mem9 Recall Hook

Queries mem9 for memories relevant to the current user message
and injects them as context before LLM calls.
