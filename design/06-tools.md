# 06 — Tool System

## 1. Overview

Every capability the agent can invoke — running a shell command, reading a file, searching the web — is modeled as a **Tool**. Tools are collected in a **Registry** that the agent session queries at each LLM turn to build the function-calling schema. The registry also handles progressive disclosure, middleware chains, skill discovery, and external plugin loading.

Key source files:

| File | Role |
|------|------|
| `internal/tools/tool.go` | `Tool` interface |
| `internal/tools/registry.go` | `Registry` — registration, lookup, execution, progressive disclosure |
| `internal/tools/progressive.go` | `ExpandHints` convenience wrapper |
| `internal/tools/skill.go` | `skillTool` + `SKILL.md` parser and discovery |
| `internal/tools/external.go` | `ExternalTool` — JSON-RPC 2.0 plugin host |
| `internal/middleware/middleware.go` | `Middleware` type |
| `internal/tools/builtin/` | All built-in tool implementations |
| `internal/app/app.go` | Wiring — creates the registry and registers everything |

## 2. Tool Interface

Every tool implements the `Tool` interface, which exposes a unique `Name`, a `Description` (short, for compact/progressive mode), a `FullDescription` (expanded mode), a `Parameters` method returning a JSON Schema for its arguments, and an `Execute` method that receives the raw JSON string the LLM produced and returns a plain-text result fed back into the conversation. Errors returned from `Execute` are treated as system-level failures; user-visible errors are returned as strings (e.g. `"Error: file not found"`).

The dual description methods support progressive disclosure (section 4). `Parameters` returns a full JSON Schema that the registry may swap for a minimal stub when the tool is in compact mode.

Source: `internal/tools/tool.go`

## 3. Registry

`Registry` is the central catalog. It provides methods to register tools (individually or in batch, preserving insertion order), look up a tool by name, and execute a tool through the middleware chain. It builds the `ToolDefinitions` slice sent to the LLM on each turn. For progressive disclosure it can expand a tool by name, detect tool-name references in arbitrary text via word-boundary matching, and combine both steps in a single `ExpandHints` call. It discovers skills from the filesystem, lists all registered tools in a human-readable format (split into "Built-in" and "Skills"), and accepts middleware via `Use`.

Source: `internal/tools/registry.go`

### Middleware chain

`Registry.Use` accepts a `middleware.Middleware`:

```go
// internal/middleware/middleware.go
type Middleware func(ctx context.Context, toolName string, args string,
    next func(context.Context, string) (string, error)) (string, error)
```

Middlewares wrap `Execute` in registration order (outermost first). The app currently registers two: **CacheMiddleware**, which caches results of read-only tools (`read_file`, `list_directory`, `search_files`, `web_search`, `web_fetch`) for 60 seconds, and **Redact**, which strips secrets from tool output before they reach the tape or LLM.

### Tool registration order (in `app.go`)

1. Core tools (shell, file ops)
2. Web tools (search, fetch, HTTP)
3. Lark tools (if `LarkChannel` is configured) — pre-expanded
4. Memory tools (if mnemos is configured)
5. Persona memory tool (if persona is configured)
6. Schedule tools (added later, on the default registry)
7. Spawn agent tool
8. Skills (discovered from `cfg.SkillsDir`)
9. External plugins (from `~/.golem/plugins/*.tool.json`)

## 4. Progressive Disclosure

To save tokens, tools that the conversation has not referenced yet are sent to the LLM in **compact mode**: a short description and a minimal empty-object parameter schema. When the LLM or user mentions a tool by name, the registry **expands** it — subsequent `ToolDefinitions` calls include the full description and the real parameter schema.

Detection uses word-boundary matching so that e.g. "file" does not false-match "read_file". Underscores are also matched as spaces, so "read file" expands `read_file`. Some tools are pre-expanded at registration time (all four Lark tools) because they are always relevant when the Lark channel is active.

## 5. Skills

Skills are markdown-based prompt injections discovered from the filesystem. They live under `<skills-dir>/<skill-name>/SKILL.md`.

### File format

```markdown
---
name: summarize-session
description: Summarize the current session
---

(markdown body — full instructions injected as the tool result)
```

Frontmatter is delimited by `---` lines and must contain `name` and `description` fields. The tool is registered as `skill_<name>`. Skills accept a single `input` string parameter but their `Execute` simply returns the markdown body — they are context injections, not executable code.

`Registry.DiscoverSkills(dir)` walks immediate subdirectories of `dir`, looking for `SKILL.md` in each. Invalid files are silently skipped.

Source: `internal/tools/skill.go`

## 6. External Plugins

External tools are standalone executables that communicate with Golem via **JSON-RPC 2.0 over stdin/stdout**.

### Manifest format (`*.tool.json`)

```json
{
  "name": "my_plugin",
  "description": "Short description",
  "full_description": "Expanded description for progressive mode",
  "parameters": { "type": "object", "properties": { "..." : "..." } },
  "command": "/path/to/executable",
  "args": ["--flag"],
  "work_dir": "/optional/cwd",
  "timeout_seconds": 30
}
```

`name` and `command` are required. If `parameters` is omitted, an empty object schema is used.

### Protocol

Each invocation sends a JSON-RPC request (one JSON object per line). The plugin responds with a single-line JSON-RPC response containing either a `result` string or an `error` object with `code` and `message` fields.

### Lifecycle

The plugin process is lazily started on the first `Execute` call and stays running across calls (long-lived, one process per tool). If the process dies, `cleanup()` is called and it restarts on the next invocation. `Close()` kills the process and is safe to call multiple times. Plugin stderr is forwarded to the host's stderr, and the stdout scanner uses a 64 KB initial buffer with a 1 MB max per line.

`LoadExternalTools(dir)` reads all `*.tool.json` files from the given directory (default `~/.golem/plugins/`), called during registry setup in `app.go`.

Source: `internal/tools/external.go`

## 7. Builtin Tools Catalog

| Tool | Description | Key Parameters |
|------|-------------|----------------|
| `shell_exec` | Execute a shell command via an `Executor` | `command` (required), `timeout` (seconds, default 30) |
| `read_file` | Read file contents with optional offset/limit; truncates at 50K chars | `path`, `offset` (line, 0-based), `limit` (line count) |
| `write_file` | Write/overwrite a file, creating parent dirs | `path`, `content` |
| `edit_file` | Find-and-replace first occurrence of exact text | `path`, `old_text`, `new_text` |
| `list_directory` | List directory entries with type/size indicators; caps at 200 entries | `path` (default `.`) |
| `search_files` | Recursive case-insensitive text search; caps at 50 hits | `path`, `pattern`, `file_glob` (optional) |
| `web_search` | Search the web (Bing scraper) | `query`, `count` (default 5, max 20) |
| `web_fetch` | Fetch a URL and extract readable text from HTML | `url`, `max_length` (default 5000) |
| `http_request` | Raw HTTP request with full control over method/headers/body; blocks sensitive headers | `url`, `method` (default GET), `headers`, `body`, `max_length` (default 50000) |
| `lark_send` | Send a text message to a Lark group chat | `chat_id`, `message` |
| `lark_list_chats` | List Lark groups the bot belongs to | (none) |
| `lark_read_doc` | Read plain text of a Feishu document; accepts full URL | `document_id` |
| `lark_write_doc` | Replace entire document content with markdown converted to Feishu blocks | `document_id`, `content` (markdown) |
| `memory_store` | Save information to persistent shared memory (mnemos vector DB) | `content`, `tags`, `key` (upsert), `source` |
| `memory_recall` | Search shared memories by relevance | `query`, `limit` (default 10) |
| `persona_memory` | Read/write the agent's own `MEMORY.md` file | `action` (`read`/`write`), `content` (for write) |
| `schedule_add` | Create a cron-scheduled task; supports standard cron, `@daily`, `@every 30m`, `CRON_TZ=` | `cron_expr`, `prompt`, `channel_name`, `channel_id`, `description` |
| `schedule_list` | List all scheduled tasks | (none) |
| `schedule_remove` | Remove a scheduled task by ID | `id` |
| `spawn_agent` | Delegate a task to an independent sub-agent (cannot spawn further agents) | `prompt`, `context` (optional) |

File tools skip binary files (detected by extension) and ignore directories like `.git`, `node_modules`, `vendor`, `__pycache__`, `.venv`, and `target` during listing and searching. All Lark tools are pre-expanded in the registry. Both doc tools auto-extract the document token from full Feishu/Lark URLs. Schedules fire prompts into isolated agent sessions at cron times.

## 8. Current Gaps

1. **No streaming tool output.** `Execute` returns a complete string. Long-running shell commands or large fetches block until completion with no incremental feedback to the user.

2. **No tool-level permissions or allow/deny lists.** All registered tools are available to every session. There is no per-chat or per-user access control beyond whether a tool is registered at all (e.g. Lark tools are only registered when `LarkChannel != nil`).

3. **No tool timeout enforcement at the registry level.** Timeouts are implemented per-tool (e.g. `ShellTool` has a configurable default) rather than as a registry-wide middleware. External plugins have a `timeout_seconds` manifest field but it is not enforced in the current code.

4. **External plugin protocol is synchronous and single-threaded.** The `ExternalTool.Execute` method holds a mutex for the entire request-response cycle, serializing all calls to a given plugin. There is no support for concurrent requests or async notifications.

5. **Skill tools are read-only prompt injections.** The `input` parameter is accepted but ignored — `Execute` always returns the static markdown body regardless of what the LLM passes. Skills cannot perform dynamic computation.

6. **No tool-result size limits at the middleware layer.** Individual tools enforce their own truncation (e.g. `read_file` at 50K chars, `web_fetch` at 5K), but there is no unified guard against a tool returning an unexpectedly large result that blows up the context window.

7. **Progressive disclosure is keyword-only.** `DetectToolHints` matches tool names as literal words. It cannot detect semantic intent (e.g. a user asking "what's in this directory?" won't auto-expand `list_directory` unless those exact words appear).
