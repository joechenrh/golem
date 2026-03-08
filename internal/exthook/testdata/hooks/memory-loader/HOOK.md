---
name: memory-loader
description: Load relevant memory before LLM calls
events:
  - before_llm_call
  - after_reset
command: ./handler.sh
timeout: 10s
---

# Memory Loader Hook

This hook queries the external memory store for context
relevant to the current user message.
