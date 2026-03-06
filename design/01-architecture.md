# Golem Architecture

## 1. Overview

Golem is an AI agent framework built around the **ReAct loop** (Reason + Act). It reads user messages from pluggable channels, runs them through an LLM that can call tools, executes those tools, feeds results back to the LLM, and repeats until the LLM produces a final answer.

The three pluggable axes are:

| Axis | Interface | Built-in implementations |
|------|-----------|--------------------------|
| **LLM** | `llm.Client` | OpenAI, Anthropic, any OpenAI-compatible via auto-registration |
| **Channel** | `channel.Channel` | CLI (interactive REPL), Lark (WebSocket), Telegram |
| **Tool** | `tools.Tool` | Shell, file ops, web search/fetch/HTTP, Lark docs, memory, scheduler, spawn_agent, skills, external plugins |

A single golem process can run **multiple agents** concurrently: one interactive CLI agent ("default") plus any number of background agents discovered from `~/.golem/agents/`. Each agent has its own config, channels, sessions, and tool registries but shares the process lifetime.

## 2. Startup Flow

Source: `cmd/golem/main.go`

The process begins by parsing CLI flags (`--model`, `--tape-dir`, `--skills-dir`, `--log-level`, `--version`) into a map of overrides, then loading the two-tier config. Global settings live in `~/.golem/config.env` (LLM keys, provider URLs), while agent-specific settings live in `~/.golem/agents/default/config.env` (behavior, channel credentials). CLI flags override both tiers. The config loader also reads persona files (SOUL.md, IDENTITY.md, USER.md, AGENTS.md, MEMORY.md). A file-only zap logger is initialized next, writing to `<tapeDir>/golem-<timestamp>.log` so log output never mixes with the interactive REPL.

With config and logging in place, `app.BuildAgent("default", cfg, logger)` wires together the full `AgentInstance` (see section 3). If `GOLEM_METRICS_PORT` is set, an HTTP server starts on that port exposing `/debug/metrics` and registers the default agent's `MetricsHook` and `SessionManager`. A startup banner displays the model name, tool count, and tape file path.

Finally, signal handling is configured: `signal.NotifyContext` creates a context cancelled on SIGINT/SIGTERM, and a `claimedLarkApps` map is initialized with the default agent's Lark app ID (if any) to prevent duplicate WebSocket connections. A plain `errgroup.Group` (deliberately not `WithContext`) launches the CLI agent and all background agents as goroutines. The CLI agent goroutine calls `defer cancel()`, so when it exits -- whether by `:quit`, EOF, or error -- the shared context is cancelled and all background agents stop. Background agent errors are logged but return `nil` to the errgroup so they never kill the CLI.

## 3. Agent Wiring

Source: `internal/app/app.go`

`app.BuildAgent` assembles an `AgentInstance` by wiring together three groups of components.

**LLM and Tape.** The LLM client is constructed by parsing a `provider:model` string, looking up the appropriate API key, and auto-registering unknown providers as OpenAI-compatible endpoints. The resulting client is wrapped in a `RateLimitedClient` (token-bucket, default 10 req/s). A `tape.FileStore` is created next, producing a JSONL file named `session-<name>-<timestamp>.jsonl` in the configured tape directory. The tape is an append-only log with an in-memory cache that auto-rotates at 50 MB.

**Hooks, Channels, and Context.** A local executor and filesystem handle shell commands from the working directory. The context strategy governs how conversation history is sent to the LLM: the default `"masking"` mode truncates large tool outputs when tokens exceed 50% of the model's context window, while `"anchor"` mode sends everything since the last anchor verbatim. Four hooks are registered on the hook bus in order: `LoggingHook` (debug logging), `SafetyHook` (blocks dangerous shell commands, SSRF, sensitive file writes), `MetricsHook` (LLM call/token/tool counters, latency tracking), and `AuditHook` (structured JSONL audit trail). The CLI channel is created only for the `"default"` agent; Lark and Telegram channels are created when their respective credentials are present.

**Tools and Sessions.** `BuildToolRegistry` registers core tools (shell, file ops), web tools, Lark tools, memory tools (mnemos), persona memory, discovered skills, and external plugins from `~/.golem/plugins/`. Two middlewares wrap every tool call: `CacheMiddleware` (60s TTL for read-only tools) and `Redact` (strips secrets from output). A schedule store backed by `~/.golem/agents/<name>/schedules.json` provides `schedule_add`, `schedule_list`, and `schedule_remove` tools. The `spawn_agent` tool is registered only on the top-level registry; sub-agents get their own tape, tool registry, and hooks but share the LLM client, and their registries intentionally omit `spawn_agent` to prevent recursive spawning. A default `Session` handles CLI input, while a `SessionManager` is created when the agent has remote channels. The session manager gives each remote chat ID an isolated session with its own tape and tool registry, and restores existing sessions from tape files matching `session-<agent>-<chatID>-<ts>.jsonl`.

The returned `AgentInstance` struct bundles everything needed to call `Run()`.

## 4. Background Agents

Source: `internal/app/app.go`

`app.DiscoverAndBuildBackgroundAgents` reads subdirectories of `~/.golem/agents/`, skipping `"default"` (already used by the CLI agent). For each remaining agent name it loads the agent-specific config and skips agents that lack remote channel credentials (no Lark or Telegram). A Lark deduplication check prevents two agents from opening duplicate WebSocket connections to the same Lark app: if the agent's `LarkAppID` is already in the `claimedLarkApps` map it is skipped, and after a successful build the app ID is added to the map. Each agent is wired through the same `BuildAgent` path as the default agent, but without a CLI channel. In the errgroup, background agents run as goroutines whose errors are logged but swallowed so a failing background agent does not take down the CLI. They share the same context as the CLI agent and stop when it exits.

## 5. Shutdown

The shutdown sequence begins when the user types `:quit` (returns `ErrQuit`, triggering `cancel()`), presses Ctrl+C or receives SIGTERM (`signal.NotifyContext` cancels the context), or the CLI channel reaches EOF. Because the CLI agent goroutine has `defer cancel()`, any exit path cancels the shared context. That cancellation propagates to all background agents, whose `channel.Start()` calls see `ctx.Done()` and return. Session cleanup in `Run()` calls `Sessions.Shutdown()`, which cancels all per-chat session contexts and clears the session map; the eviction loop goroutine exits on `ctx.Done()`. The scheduler exits its tick loop on the same signal. Once all goroutines finish, `g.Wait()` returns in `main()` and `Goodbye!` is printed.

Key design choice: `errgroup.Group` is used without `WithContext` because the CLI agent needs to be the one to cancel the context (via `defer cancel()`), not the errgroup itself. Background agents return `nil` even on error to avoid cancelling the CLI.

## 6. Key Data Flow

Trace of a message from user input to response, using the CLI channel as the example. For remote channel message routing, see `design/07-channels.md`.

```
User types "fix the bug in main.go"
        |
        v
[1] cli.Channel.Start() reads from stdin, wraps text in
    IncomingMessage{ChannelName:"cli"}, sends to inCh (buffered chan, cap 100)
        |
        v
[2] AgentInstance.processMessages() reads from inCh.
    ChannelName == "cli" -> dispatches inline (not queued).
        |
        v
[3] processMessage() selects the default Session.
    Channel supports streaming -> creates tokenCh, spawns
    ch.SendStream() goroutine. Calls sess.HandleInputStream().
        |
        v
[4] Session.HandleInputStream() -> router.RouteUser(text).
    Not a ":" command -> calls runReActLoop(stream=true, tokenCh).
        |
        v
[5] runReActLoop() -- iteration 0:
    a. executeLLMCall(): reads tape, builds context via ContextStrategy,
       assembles system prompt (persona or flat), collects tool definitions
       (progressive disclosure: unexpanded tools get compact schemas).
    b. llm.Client.ChatStream() -> tokens flow to tokenCh -> CLI renders live.
    c. Response has ToolCalls [{name:"shell_exec", args:{"command":"..."}}].
        |
        v
[6] processToolCalls():
    a. Records assistant message + tool calls to tape.
    b. Auto-expands called tool schemas (progressive disclosure).
    c. Executes tools in parallel via errgroup.
       Each tool call: hooks.Emit(BeforeToolExec) -> SafetyHook checks ->
       Registry.Execute (middleware chain: cache -> redact -> tool.Execute)
       -> hooks.Emit(AfterToolExec).
    d. Records tool results to tape in original order.
    e. Tracks per-tool failure count; after 3 failures, injects a
       "reconsider" nudge.
        |
        v
[7] runReActLoop() -- iteration 1:
    LLM sees tool results in context, returns Content with no ToolCalls.
    looksLikePlan() check: if the response starts with "I'll..." or
    "Let me...", a nudge is injected (up to 2x) to force tool use.
    Otherwise -> processAssistantResponse(): scans for embedded colon
    commands, records final answer to tape, returns content.
        |
        v
[8] tokenCh is closed, SendStream goroutine finishes rendering.
    processMessage() returns false (no quit).
    Control returns to processMessages() which reads next inCh message.
```

For **remote channels** (Lark/Telegram), step [2] differs: messages are fanned out to per-channelID queues (`chatQueues` map). Each queue is drained by a dedicated goroutine, serializing messages within a chat while running different chats in parallel. Step [3] uses `SessionManager.GetOrCreate(channelID)` to obtain a per-chat `Session` with isolated tape and tools.

For **scheduled tasks**, the scheduler's `fire()` creates an ephemeral session via `appSessionFactory.HandleScheduledPrompt()`, runs the prompt through it, and sends the response back via the target channel.

## 7. Current Gaps

**Duplicate tool factory logic.** `BuildAgent` contains three near-identical closures that build tool registries (the SessionManager factory, the scheduler factory, and the sub-agent builder). Each duplicates the schedule-tool registration block. A single `ToolFactoryFunc` field on config or a builder method would eliminate this.

**No graceful drain for in-flight LLM calls.** When context is cancelled, any in-progress `llm.Chat` or `llm.ChatStream` call is interrupted immediately. There is no grace period to let a nearly-complete response finish. A user pressing Ctrl+C during a long tool-calling chain loses all intermediate work.

**MetricsHook latency tracking is single-threaded.** `llmCallStart` is a single `time.Time` field protected by a mutex. If two sessions call the LLM concurrently (possible with remote channels), the start time of the second call overwrites the first, producing incorrect latency measurements. This should be keyed by a call ID or goroutine ID.

**Session eviction discards context silently.** `EvictIdle` calls `Summarize()` before eviction, but if the summarization LLM call fails (network error, rate limit), the session is still evicted and the summary is lost. The tape file persists on disk, but restored sessions from that tape will lack the summary context.

**No backpressure on inCh.** The incoming message channel has a fixed buffer of 100. If a Lark channel receives a burst of messages faster than the agent can process them, senders will block. There is no mechanism to drop or reject messages under load, nor any metric tracking queue depth.

**Progressive disclosure state is not persisted.** `Registry.expanded` is an in-memory map. When a session is restored from tape, all tools revert to compact schemas even if the previous session had expanded them. The LLM has to re-discover and re-expand tools.

**Background agent errors are silently swallowed.** Background agent errors are logged but always return `nil` to the errgroup. There is no mechanism to restart a crashed background agent -- it stays dead for the rest of the process lifetime.

**Duplicate step numbering in main.go.** Signal handling and `claimedLarkApps` initialization are both labeled step 7 in the source comments.
