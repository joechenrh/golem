# 10 — Memory System

## 1. Overview

Golem has two independent memory subsystems. **Shared memory** is an optional external integration (default: [mem9](https://mem9.ai)) installed as external tool plugins and hooks. **Persona memory** is backed by a local `MEMORY.md` file on disk, scoped per-agent, and exposed through the `persona_memory` tool.

Shared memory provides persistent storage with search. Persona memory is a simple read/write scratchpad for an agent's curated notes.

Shared memory tools live in `~/.golem/plugins/` as external JSON-RPC plugins. The reference implementation is in `configs/mem9/`. Persona memory is built-in at `internal/tools/builtin/persona_self_tool.go`.

## 2. Shared Memory (External — mem9)

Shared memory is no longer a built-in Go subsystem. It is provided by external tool plugins that communicate with a cloud memory API via JSON-RPC 2.0 over stdin/stdout.

The reference implementation uses [mem9](https://mem9.ai) and consists of:

- **Handler script** (`configs/mem9/mem9_handler.py`): Python3 script that implements both tool mode (long-running JSON-RPC server) and hook mode (one-shot stdin/stdout).
- **Tool manifests** (`configs/mem9/plugins/*.tool.json`): Five JSON manifest files that register the handler as external tools.
- **Context hook** (`configs/mem9/hooks/mem9-context/`): Injects relevant memories before LLM calls and saves session summaries on reset.
- **Setup skill** (`configs/mem9/skills/mem9/SKILL.md`): Interactive onboarding guide.

### Configuration

Two environment variables control the mem9 integration:

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `MEM9_API_URL` | No | `https://api.mem9.ai` | mem9 API base URL |
| `MEM9_SPACE_ID` | Yes | — | Space / tenant identifier |

### Installation

See `configs/mem9/README.md` for setup instructions. In short: copy the handler script and tool manifests to `~/.golem/plugins/`, replacing the `__GOLEM_HOME__` placeholder with the actual path, and optionally install the context hook.

## 3. Shared Memory Tools

Five tools are available when the mem9 plugin is installed:

### memory_store

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `content` | string | yes | The information to remember |
| `tags` | string[] | no | 1-3 short categorization tags |
| `source` | string | no | Source agent identifier (default `"golem"`) |

Returns `"Memory stored (id: <uuid>)"` on success.

### memory_search

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `query` | string | yes | Search query (2-3 keywords recommended) |
| `limit` | integer | no | Max results (default 10) |

Returns a formatted listing with content, source, tags, and relevance score.

### memory_get

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `id` | string | yes | Memory ID to retrieve |

Returns full memory content and metadata as JSON.

### memory_update

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `id` | string | yes | Memory ID to update |
| `content` | string | yes | New content |

Returns `"Memory updated (id: <id>)"` on success.

### memory_delete

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `id` | string | yes | Memory ID to delete |

Returns `"Memory deleted (id: <id>)"` on success.

## 4. Context Hooks

Memory integration uses two focused hooks (split from the former `mem9-context`):

- **`mem9-recall`** (`before_llm_call`): Searches mem9 for memories relevant to the current user message and injects them as context.
- **`mem9-save`** (`after_reset`): Stores the session summary in mem9 with `session-summary` tag for future recall.

Both hooks are optional — memory tools work independently without them.

## 5. Persona Memory Tool

Source: `internal/tools/builtin/persona_self_tool.go`

This tool operates on local files (`~/.golem/agents/<name>/MEMORY.md`), not the external memory API.

### persona_memory

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | `"read"` or `"write"` | yes | Operation to perform |
| `content` | string | write only | Full replacement content for MEMORY.md |

A **read** returns file contents, or a message indicating the file does not exist or is empty. A **write** atomically replaces the file via `os.WriteFile`, creating parent directories if needed.

### How it differs from shared memory tools

| Dimension | `persona_memory` | `memory_store` / `memory_search` |
|-----------|-------------------|----------------------------------|
| Storage | Local filesystem | Cloud API (mem9) |
| Scope | Single agent | Shared across all agents and sessions |
| Search | None (whole-file read) | API-powered relevance search |
| Structure | Free-form Markdown | Structured records (content, tags, source) |
| Persistence | Survives restarts, local to host | Cloud-hosted, survives host loss |
| Size guidance | Under 200 lines | Unbounded |
| Installation | Built-in | External plugin (requires setup) |

`persona_memory` is designed for an agent's curated self-knowledge (project conventions, user preferences). The shared memory tools are for factual memories that benefit from search and cross-agent sharing.

## 6. Extensibility

Because shared memory is now an external plugin, it can be swapped for any backend that implements the same JSON-RPC protocol. To use a different memory provider:

1. Write a handler script that accepts the same JSON-RPC `execute` method with the same parameter schemas.
2. Update the tool manifest files to point to the new handler.
3. Optionally update the context hook.

No Go code changes are needed.
