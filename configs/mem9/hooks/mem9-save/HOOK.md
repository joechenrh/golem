---
name: mem9-save
description: Save session summaries to mem9 on reset
events: [after_reset]
command: ./handler.py
timeout: 15s
---

# mem9 Save Hook

Saves session summaries to mem9 when a session is reset,
so future sessions can recall what was discussed.
