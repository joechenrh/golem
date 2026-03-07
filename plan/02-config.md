# Step 2: Configuration

## Scope

Hierarchical configuration system: CLI flags > env vars > `.env.local` > defaults.

## File

`internal/config/config.go`

## Key Points

### Config Struct

```go
type Config struct {
    // LLM
    Model     string            // e.g. "openai:gpt-4o", "anthropic:claude-sonnet-4-20250514"
    APIKeys   map[string]string // provider name -> API key

    // Agent behavior
    MaxToolIter  int           // max tool-calling iterations per turn (default: 15)
    ShellTimeout time.Duration // shell command timeout (default: 30s)

    // Storage
    TapeDir   string // directory for tape JSONL files (default: ~/.golem/tapes)

    // Channels (stubs for now)
    TelegramToken string
    TelegramACL   []int64
    LarkAppID     string
    LarkAppSecret string
    LarkWebhookPort int

    // Memory (stub for now)
    MnemosURL     string
    MnemosSpaceID string

    // Logging
    LogLevel string // "debug", "info", "warn", "error"
}
```

### Loading Precedence

```
1. CLI flags (--model, --tape-dir, etc.)     ← highest
2. Environment variables (GOLEM_MODEL, OPENAI_API_KEY, etc.)
3. .env.local file (loaded via godotenv)
4. Hardcoded defaults                        ← lowest
```

### Key Functions

```go
// Load reads config from all sources with precedence.
// flagOverrides are CLI flag values (only non-empty ones override).
func Load(flagOverrides map[string]string) (*Config, error)

// ModelProvider extracts the provider prefix from config.Model.
// "openai:gpt-4o" → ("openai", "gpt-4o")
// "gpt-4o" → ("openai", "gpt-4o")  // default provider
func (c *Config) ModelProvider() (provider, model string)
```

### Environment Variable Mapping

| Config Field | Env Var | Default |
|---|---|---|
| Model | `GOLEM_MODEL` | `openai:gpt-4o` |
| APIKeys["openai"] | `OPENAI_API_KEY` | — |
| APIKeys["anthropic"] | `ANTHROPIC_API_KEY` | — |
| MaxToolIter | `GOLEM_MAX_TOOL_ITER` | `15` |
| ShellTimeout | `GOLEM_SHELL_TIMEOUT` | `30s` |
| TapeDir | `GOLEM_TAPE_DIR` | `~/.golem/tapes` |
| LogLevel | `GOLEM_LOG_LEVEL` | `info` |

### Validation

`Load()` validates the config and returns an error for:
- Invalid `LogLevel` (must be one of: debug, info, warn, error)
- `MaxToolIter <= 0`
- `ShellTimeout <= 0`
- Empty `Model`
- Model format with more than one colon (e.g., `"a:b:c"`)

`.env.local` parse errors are surfaced (file-not-found is silently ignored).

### Provider Routing

`Config.ModelProvider()` was **removed** to avoid duplicating `llm.ParseModelProvider()`. Callers should use `llm.ParseModelProvider(cfg.Model)` directly.

### Design Decisions

- No TOML/YAML config file for now — env vars + `.env.local` is sufficient for Phase 1-2
- `~` in paths is expanded to `os.UserHomeDir()`
- API keys are stored in a map keyed by provider name for easy lookup
- Tests use `t.Setenv()` (not `os.Setenv`) for automatic cleanup and parallel-safety awareness

## Done When

- `config.Load(nil)` returns a Config with defaults
- Setting `GOLEM_MODEL=anthropic:claude-sonnet-4-20250514` overrides the model
- `.env.local` file values are loaded
- Invalid log level / model format / zero iterations returns a descriptive error
