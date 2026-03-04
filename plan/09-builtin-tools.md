# Step 9: Built-in Tools

## Scope

Concrete tool implementations for shell execution and file operations. Maps to crabclaw's `tools/file_ops.rs` + shell execution in the tool layer.

## Files

- `internal/tools/builtin/shell_tool.go` — Shell execution tool (uses `executor.Executor`)
- `internal/tools/builtin/file_ops.go` — File operation tools (uses `fs.FS`)

## Key Points

### Shell Tool (`shell_tool.go`)

**Name**: `shell_exec`

**Parameters**:
```json
{
  "type": "object",
  "properties": {
    "command": {"type": "string", "description": "Shell command to execute"},
    "timeout": {"type": "integer", "description": "Timeout in seconds (optional, default 30)"}
  },
  "required": ["command"]
}
```

**Execute**: Parse args JSON, call `executor.Execute()`, return `executor.FormatResult()`.

The tool receives an `executor.Executor` interface at construction — it doesn't know if it's local, Docker, or noop.

### File Operations (`file_ops.go`)

All file tools use the `fs.FS` interface. Workspace sandboxing is handled by the `LocalFS` implementation — tools don't know about path resolution or sandbox logic.

#### `read_file`

**Parameters**: `{"path": "string", "offset": "integer?", "limit": "integer?"}`

- Reads file content, returns as string
- Max 50,000 characters (truncated with notice)
- Optional offset/limit for reading portions of large files
- Binary file detection by extension (.png, .jpg, .exe, etc.) → returns "[binary file]"

#### `write_file`

**Parameters**: `{"path": "string", "content": "string"}`

- Creates parent directories as needed (`os.MkdirAll`)
- Writes full file content (overwrites if exists)
- Returns confirmation with file path and byte count

#### `edit_file`

**Parameters**: `{"path": "string", "old_text": "string", "new_text": "string"}`

- Targeted string replacement within a file
- Reads file, replaces first occurrence of `old_text` with `new_text`
- Fails if `old_text` not found (returns error message for LLM)
- Returns confirmation with line range affected

#### `list_directory`

**Parameters**: `{"path": "string"}`

- Lists directory contents with type indicators (file/dir) and sizes
- Skips `.git`, `node_modules`, `target`, `vendor` directories
- Max 200 entries
- Format: `[dir]  subdir/\n[file] main.go (1.2 KB)`

#### `search_files`

**Parameters**: `{"path": "string", "pattern": "string", "file_glob": "string?"}`

- Recursive file content search (case-insensitive)
- Returns matching lines with file paths and line numbers
- Max 50 matches
- Skips binary files and ignored directories (.git, node_modules, etc.)
- Optional file glob filter (e.g., `"*.go"`)
- Format: `path/to/file.go:42: matching line content`

### Filesystem Abstraction (`internal/fs/`)

File tools receive an `fs.FS` interface at construction:

```go
type ReadFileTool struct {
    fs fs.FS
}

func NewReadFileTool(filesystem fs.FS) *ReadFileTool
```

The `LocalFS` implementation handles:
- Path resolution relative to workspace root
- Sandbox enforcement (reject `../` traversal beyond root)
- `filepath.Abs()` + `strings.HasPrefix()` after canonicalization

Tools just call `fs.ReadFile(path)` — they don't know about sandboxing.

### Stub Tools (for later phases)

Minimal stubs that return "not implemented yet":
- `internal/tools/builtin/web.go` — `web_fetch`, `web_search`
- `internal/tools/builtin/memory_tools.go` — `memory_store`, `memory_search`, `memory_get`, `memory_update`, `memory_delete`
- `internal/tools/builtin/schedule.go` — `schedule_add`, `schedule_list`, `schedule_remove`

Each stub registers with proper name/description/parameters but `Execute()` returns `"This tool is not yet implemented."`

## Design Decisions

- File tools are conservative — read limits, search limits, binary detection all prevent context window pollution
- Workspace sandbox is a hard boundary, not advisory — the tool refuses to operate outside it
- `edit_file` uses exact string matching, not regex — simpler, less error-prone for the agent
- Stubs are registered so the LLM knows they exist but can't use them yet

## Done When

- `shell_exec` runs `echo hello` and returns formatted output
- `read_file` reads a file within workspace, rejects paths outside
- `write_file` creates a new file with parent directories
- `edit_file` replaces text in an existing file
- `list_directory` shows directory contents with sizes
- `search_files` finds matching lines across files
- All tools are registered in the Registry
