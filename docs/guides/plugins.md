# External Plugins

Golem supports two types of external tool integrations: **custom plugins** using Golem's JSON-RPC protocol, and **MCP servers** using the standard Model Context Protocol.

## Custom Plugins

External plugins are standalone executables that communicate with Golem via JSON-RPC 2.0 over stdin/stdout.

### Creating a Plugin

1. Create a manifest file in `~/.golem/plugins/`:

```json
{
  "name": "my_plugin",
  "description": "Short description",
  "full_description": "Expanded description for progressive mode",
  "parameters": {
    "type": "object",
    "properties": {
      "input": {
        "type": "string",
        "description": "The input to process"
      }
    },
    "required": ["input"]
  },
  "command": "/path/to/my-plugin",
  "args": ["--flag"],
  "timeout_seconds": 30
}
```

Save as `~/.golem/plugins/my_plugin.tool.json`.

### Manifest Fields

| Field | Required | Description |
|---|---|---|
| `name` | Yes | Unique tool name |
| `command` | Yes | Path to executable |
| `description` | No | Short description (compact mode) |
| `full_description` | No | Full description (expanded mode) |
| `parameters` | No | JSON Schema for arguments (defaults to empty object) |
| `args` | No | Command-line arguments |
| `work_dir` | No | Working directory |
| `timeout_seconds` | No | Execution timeout |

### Protocol

Each invocation sends a JSON-RPC 2.0 request (one JSON object per line):

```json
{"jsonrpc": "2.0", "id": 1, "method": "execute", "params": {"input": "hello"}}
```

The plugin responds with a JSON-RPC response:

```json
{"jsonrpc": "2.0", "id": 1, "result": "processed: hello"}
```

Or an error:

```json
{"jsonrpc": "2.0", "id": 1, "error": {"code": -1, "message": "something went wrong"}}
```

### Lifecycle

- The plugin process starts lazily on first invocation and stays running across calls
- If the process dies, it restarts automatically on the next invocation
- Plugin stderr is forwarded to the host's stderr
- Max line size: 1 MB

## MCP Servers

Golem also supports [Model Context Protocol](https://modelcontextprotocol.io/) servers as a tool source. One MCP server can expose multiple tools.

### Adding an MCP Server

Create a manifest in `~/.golem/plugins/`:

```json
{
  "command": "npx",
  "args": ["-y", "@modelcontextprotocol/server-filesystem", "/path"],
  "env": {}
}
```

Save as `~/.golem/plugins/my_server.mcp.json`.

### Manifest Fields

| Field | Required | Description |
|---|---|---|
| `command` | Yes | Server executable |
| `args` | No | Command-line arguments |
| `env` | No | Environment variables (supports `$VAR` expansion) |

### How It Works

1. Golem launches the MCP server subprocess
2. Sends `initialize` + `notifications/initialized` handshake
3. Calls `tools/list` to discover available tools
4. Each discovered tool is registered in Golem's tool registry
5. Tool invocations use `tools/call` with the tool name and arguments
6. The subprocess stays running for the session lifetime

All MCP tools go through the normal middleware chain (caching, redaction).

## Architecture

See [Tool System](../design/06-tools.md) for the full technical design of external plugins and MCP servers.
