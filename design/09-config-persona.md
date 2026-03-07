# 09 — Configuration & Persona Identity System

## 1. Overview

Golem uses a **two-tier configuration** model (global + per-agent) loaded from `.env` files, shell environment variables, and CLI flags. On top of configuration, a **three-layer persona identity** system lets each agent carry a persistent personality, behavioral rules, and self-managed knowledge.

```
~/.golem/
├── config.env                     # Global tier (LLM, skills, web)
├── USER.md                        # Shared user profile (all agents read this)
└── agents/
    ├── alice/
    │   ├── config.env             # Agent tier (behavior, channels, storage)
    │   ├── SOUL.md                # Layer 1 — core personality
    │   ├── AGENTS.md              # Layer 2 — behavioral rules
    │   ├── MEMORY.md              # Layer 3 — persistent knowledge (agent-writable)
    │   └── system-prompt.md       # Legacy fallback (used when no SOUL.md)
    └── bob/
        └── ...
```

## 2. Config Fields

All fields live in `config.Config`. Source: `internal/config/config.go`.

| Group | Field | Env Var | Type | Default | Tier |
|---|---|---|---|---|---|
| LLM | `Model` | `GOLEM_MODEL` | `string` | `"openai:gpt-4o"` | Global |
| LLM | `APIKeys` | `<PROVIDER>_API_KEY` | `map[string]string` | — | Global |
| LLM | `BaseURLs` | `<PROVIDER>_BASE_URL` | `map[string]string` | — | Global |
| Behavior | `MaxToolIter` | `GOLEM_MAX_TOOL_ITER` | `int` | `15` | Agent |
| Behavior | `MaxOutputTokens` | `GOLEM_MAX_OUTPUT_TOKENS` | `int` | `4096` | Agent |
| Behavior | `Temperature` | `GOLEM_TEMPERATURE` | `*float64` | `nil` (provider default) | Agent |
| Behavior | `ShellTimeout` | `GOLEM_SHELL_TIMEOUT` | `time.Duration` | `30s` | Agent |
| Behavior | `ContextStrategy` | `GOLEM_CONTEXT_STRATEGY` | `string` | `"masking"` | Agent |
| Behavior | `Executor` | `GOLEM_EXECUTOR` | `string` | `"local"` | Agent |
| Storage | `TapeDir` | `GOLEM_TAPE_DIR` | `string` | `~/.golem/tapes` | Agent |
| Storage | `SkillsDir` | `GOLEM_SKILLS_DIR` | `string` | `.agent/skills` | Global |
| Channels | `TelegramToken` | `TELEGRAM_BOT_TOKEN` | `string` | `""` | Agent |
| Channels | `TelegramACL` | `TELEGRAM_ALLOW_FROM` | `[]int64` | `nil` | Agent |
| Channels | `LarkAppID` | `LARK_APP_ID` | `string` | `""` | Agent |
| Channels | `LarkAppSecret` | `LARK_APP_SECRET` | `string` | `""` | Agent |
| Channels | `LarkVerifyToken` | `LARK_VERIFY_TOKEN` | `string` | `""` | Agent |
| Memory | `MnemosDBHost` | `MNEMO_DB_HOST` | `string` | `""` | Agent |
| Memory | `MnemosDBUser` | `MNEMO_DB_USER` | `string` | `""` | Agent |
| Memory | `MnemosDBPass` | `MNEMO_DB_PASS` | `string` | `""` | Agent |
| Memory | `MnemosDBName` | `MNEMO_DB_NAME` | `string` | `"mnemos"` | Agent |
| Memory | `MnemosAutoEmbedModel` | `MNEMO_AUTO_EMBED_MODEL` | `string` | `""` | Agent |
| Memory | `MnemosAutoEmbedDims` | `MNEMO_AUTO_EMBED_DIMS` | `int` | `1024` | Agent |
| Sessions | `MaxSessions` | `GOLEM_MAX_SESSIONS` | `int` | `100` | Agent |
| Sessions | `SessionIdleTime` | `GOLEM_SESSION_IDLE_TIME` | `time.Duration` | `24h` | Agent |
| Observability | `MetricsPort` | `GOLEM_METRICS_PORT` | `string` | `""` (disabled) | Global |
| Observability | `LLMRateLimit` | `GOLEM_LLM_RATE_LIMIT` | `int` | `10` | Global |
| Web | `WebSearchBackend` | `GOLEM_WEB_SEARCH_BACKEND` | `string` | `"bing"` | Global |
| Logging | `LogLevel` | `GOLEM_LOG_LEVEL` | `string` | `"info"` | Agent |

## 3. Two-Tier Loading

`Load(agentName, flagOverrides)` reads two independent `.env` files via `godotenv.Read` without polluting `os.Environ`. The **global tier** (`~/.golem/config.env`) holds LLM model, API keys/base URLs, skills dir, rate limit, metrics port, and web search backend. The **agent tier** (`~/.golem/agents/<name>/config.env`) holds behavior settings, channel credentials, storage paths, memory config, sessions, and logging. Each tier gets its own `envLookup` instance, so a global-tier variable read through the agent lookup (or vice versa) simply misses and falls back to the default — the tiers do not bleed into each other.

When `agentName` is `""`, only the global tier is loaded and agent-tier fields receive their hardcoded defaults.

## 4. Precedence

Within each tier the resolution order is:

```
CLI flags  >  shell environment  >  config.env file  >  hardcoded default
```

### How `envLookup` works

`envLookup` is a `map[string]string` built from one tier's `.env` file. Its `get(key)` method checks `os.Getenv(key)` first (shell environment wins), then the map (`.env` file value), and returns `("", false)` on miss so the caller applies the hardcoded default. Typed helpers (`str`, `integer`, `optFloat64`, `duration`, `int64List`) wrap `get` and parse the raw string, falling through to the default on parse failure.

### CLI flag overrides

`applyFlagOverrides` runs after the struct is populated. It overwrites a fixed set of string fields (`model`, `tape-dir`, `skills-dir`, `log-level`, `context-strategy`, `executor`) from the `flagOverrides` map when the value is non-empty. This gives CLI flags the highest precedence.

## 5. Provider Key Discovery

`collectProviderKeys` scans for API keys and base URLs using a naming convention rather than an explicit list:

```
<PROVIDER>_API_KEY  -->  cfg.APIKeys["<provider>"]
<PROVIDER>_BASE_URL -->  cfg.BaseURLs["<provider>"]
```

The provider name is lowercased (`OPENAI_API_KEY` becomes key `"openai"`). Shell environment (`os.Environ()`) is scanned first, and global `config.env` only fills keys not already set. This lets users set `OPENAI_API_KEY` in the shell while keeping `ANTHROPIC_API_KEY` in `config.env`, with no explicit provider registration needed.

## 6. Persona System

The persona system gives each agent a layered identity assembled into the LLM system prompt. It is implemented in `loadPersona` (config loading) and `buildPersonaPrompt` (prompt assembly). Source: `internal/config/config.go`, `internal/agent/session.go`.

### Three Layers

| Layer | File(s) | Location | Purpose |
|---|---|---|---|
| **1 — Identity** | `SOUL.md` | `~/.golem/agents/<name>/` | Core personality and voice. Activates the persona system (`HasPersona()` checks `Soul != ""`). |
| | `USER.md` | `~/.golem/` (global) | Who the agent serves. Shared across all agents. Optional. |
| **2 — Operations** | `AGENTS.md` | `~/.golem/agents/<name>/` | Behavioral rules, constraints, interaction style. Optional. |
| **3 — Knowledge** | `MEMORY.md` | `~/.golem/agents/<name>/` | Persistent curated knowledge. Read at startup; writable by the agent at runtime via the `persona_memory` tool. Optional. |

### System Prompt Assembly

When `HasPersona()` is true, `buildPersonaPrompt()` assembles the system prompt in three major sections. The **Identity** section contains the SOUL.md content, optionally followed by the USER.md user profile. The **Operations** section includes AGENTS.md behavioral rules (if present) followed by built-in tool-use instructions that are always present. The **Knowledge** section describes the memory system architecture and includes current MEMORY.md content. Finally, an **Environment** block appends the working directory and current time.

### Fallback: Legacy `system-prompt.md`

When no `SOUL.md` exists (`HasPersona()` returns false), `buildFlatPrompt()` is used instead. This loads a flat prompt from `~/.golem/agents/<name>/system-prompt.md` (read during `Load`, stored in `Config.SystemPrompt`), or `.agent/system-prompt.md` in the working directory (read at prompt-build time). The flat prompt prepends "You are golem, a helpful coding assistant" and generic tool-use instructions.

### `persona_memory` Tool

See [10-memory.md](10-memory.md) for details on the `persona_memory` tool.

## 7. Agent Discovery

`DiscoverAgents()` reads `~/.golem/agents/` and returns the name of every subdirectory. It is called by `main()` after building the default CLI agent. For each discovered agent name, `Load(name, nil)` builds its config. If the config has remote channels (`HasRemoteChannels()` — Lark or Telegram credentials present), a background `AgentInstance` is built and run in a goroutine. A `claimedLarkApps` map in `main()` deduplicates Lark app IDs so two agent configs sharing the same Lark app do not open duplicate WebSocket connections. Agents without remote channels are silently skipped (they would only be reachable via CLI, which the default agent already serves).

## 8. Validation

`Config.validate()` runs after loading and rejects invalid configurations with descriptive errors. It enforces that `LogLevel` is one of `debug`, `info`, `warn`, or `error`; that `MaxToolIter`, `ShellTimeout`, `LLMRateLimit`, `MaxSessions`, and `SessionIdleTime` are all positive; that `Temperature` (if set) falls within `[0, 2]`; that `Model` is non-empty with at most one `:` separator (enforcing `"provider:model"` or `"model"` format); and that `ContextStrategy` is one of `anchor`, `masking`, or `hybrid`. Validation runs before `Load` returns, so callers never receive an invalid `*Config`.

## 9. Current Gaps

- **No hot-reload.** Config is loaded once at startup. Changing `config.env` or persona files requires a restart (except `MEMORY.md`, which the agent writes directly to disk).
- **No per-agent model override.** `GOLEM_MODEL` is global-tier only; all agents share the same model. An agent-tier model override would allow mixing cheap/expensive models.
- **No config validation for channel credentials.** Partial Lark config (e.g., `LARK_APP_ID` set but `LARK_APP_SECRET` missing) is silently accepted. `HasRemoteChannels()` checks both, but the gap is not surfaced as an error.
- **No schema for persona files.** SOUL.md, AGENTS.md, etc. are free-form markdown. There is no validation that the content is well-formed or within a size budget.
- **Temperature not overridable by CLI flag.** `applyFlagOverrides` only handles string fields; `Temperature` (optional float pointer) has no flag path.
- **MEMORY.md size is advisory.** The 200-line limit is mentioned in the system prompt but not enforced by the `persona_memory` tool.
- **`hybrid` context strategy accepted but not implemented.** `validate()` allows `"hybrid"` but only `anchor` and `masking` strategies exist in `internal/ctxmgr/`.
