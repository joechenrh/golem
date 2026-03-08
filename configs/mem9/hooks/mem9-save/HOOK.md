---
name: mem9-save
description: Save session summaries and dropped context to mem9
events: [after_reset, context_dropped]
command: ./handler.py
timeout: 15s
---

# mem9 Save Hook

Saves session summaries to mem9 when a session is reset or evicted,
and saves dropped context when the context window overflows,
so future sessions can recall what was discussed.
