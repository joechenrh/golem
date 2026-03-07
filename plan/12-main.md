# Step 12: Main Entry Point

## Scope

Wire all components together — config, LLM client, tape, tools, router, agent loop, channels. Handle CLI flags and graceful shutdown.

## File

`cmd/golem/main.go`

## Key Points

### CLI Flags

```
Usage: golem [flags]

Flags:
  --model string       LLM model (e.g. "openai:gpt-4o") (env: GOLEM_MODEL)
  --channel string     Channel to run: cli, telegram, lark, all (default: cli)
  --tape-dir string    Directory for tape files (env: GOLEM_TAPE_DIR)
  --log-level string   Log level: debug, info, warn, error (env: GOLEM_LOG_LEVEL)
  --help               Show help
  --version            Show version
```

Use Go's `flag` package (stdlib) — no need for cobra/urfave for this simple set.

### Startup Sequence

```go
func main() {
    // 1. Parse CLI flags
    flags := parseFlags()

    // 2. Load config (flags override env override .env override defaults)
    cfg, err := config.Load(flags)

    // 3. Initialize logger
    logger := initLogger(cfg.LogLevel)

    // 4. Initialize LLM client
    provider, model := cfg.ModelProvider()
    llmClient, err := llm.NewClient(provider, cfg.APIKeys[string(provider)])

    // 5. Initialize tape store
    tapePath := filepath.Join(cfg.TapeDir, "session-"+timestamp+".jsonl")
    tapeStore, err := tape.NewFileStore(tapePath)

    // 6. Initialize executor and filesystem
    exec := executor.NewLocal(workDir)   // or executor.NewNoop() based on config
    filesystem := fs.NewLocalFS(workDir)

    // 7. Initialize context strategy
    ctxStrategy, err := ctxmgr.NewContextStrategy(cfg.ContextStrategy)

    // 8. Build hook bus
    hookBus := hooks.NewBus()
    hookBus.Register(hooks.NewLoggingHook(logger))
    // Future: hookBus.Register(memory.NewMemoryHook(memClient))
    // Future: hookBus.Register(hooks.NewSafetyHook(cfg))

    // 9. Build tool registry
    registry := tools.NewRegistry()
    registry.RegisterAll(
        builtin.NewShellTool(exec, cfg.ShellTimeout),
        builtin.NewReadFileTool(filesystem),
        builtin.NewWriteFileTool(filesystem),
        builtin.NewEditFileTool(filesystem),
        builtin.NewListDirectoryTool(filesystem),
        builtin.NewSearchFilesTool(filesystem),
        // stubs
        builtin.NewMemoryStoreTool(),
        builtin.NewMemorySearchTool(),
        // ... other stubs
    )
    registry.DiscoverSkills(filepath.Join(config.GolemHome(), "skills"))           // global scope
    registry.DiscoverSkills(filepath.Join(config.GolemHome(), "agents", name, "skills")) // agent scope

    // 10. Create agent loop
    agent := agent.New(llmClient, registry, tapeStore, ctxStrategy, hookBus, cfg, logger)

    // 8. Setup signal handling
    ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer cancel()

    // 9. Create message channel
    inCh := make(chan channel.IncomingMessage, 100)

    // 10. Start chosen channel(s)
    // ... (see below)

    // 11. Process messages from inCh
    // ... (see below)

    // 12. Wait for shutdown
}
```

### Message Processing Loop

```go
// channelMap maps channelID prefix → Channel implementation
channelMap := map[string]channel.Channel{}

go func() {
    for msg := range inCh {
        ch := channelMap[msg.ChannelName]
        if ch == nil {
            logger.Error("unknown channel", zap.String("name", msg.ChannelName))
            continue
        }
        go processMessage(ctx, agent, ch, msg)
    }
}()

func processMessage(ctx context.Context, agent *agent.AgentLoop, ch channel.Channel, msg channel.IncomingMessage) {
    // Send typing indicator
    ch.SendTyping(ctx, msg.ChannelID)

    if ch.SupportsStreaming() {
        tokenCh := make(chan string, 100)
        go ch.SendStream(ctx, msg.ChannelID, tokenCh)
        agent.HandleInputStream(ctx, msg, tokenCh)
    } else {
        response, err := agent.HandleInput(ctx, msg)
        if err != nil {
            ch.Send(ctx, channel.OutgoingMessage{ChannelID: msg.ChannelID, Text: "Error: " + err.Error()})
            return
        }
        ch.Send(ctx, channel.OutgoingMessage{ChannelID: msg.ChannelID, Text: response})
    }
}
```

### Channel Startup

```go
switch cfg.Channel {
case "cli":
    cliCh := cli.New()
    channelMap["cli"] = cliCh
    cliCh.Start(ctx, inCh)  // blocks until ctx done
case "telegram":
    // future
case "lark":
    // future
case "all":
    // start all configured channels in goroutines
}
```

### Graceful Shutdown

1. `signal.NotifyContext` catches SIGINT/SIGTERM → cancels context
2. Channel's `Start()` returns when context is cancelled
3. Close `inCh` → message processing loop exits
4. Log "Goodbye" and exit

### Startup Banner

```
golem v0.1.0
Model: openai:gpt-4o
Tools: 6 registered, 0 skills
Tape:  ~/.golem/tapes/session-20260304-100000.jsonl
Type ,help for commands, ,quit to exit.
```

## Design Decisions

- Use `flag` package, not cobra — this is a simple CLI, not a complex multi-command tool
- Single goroutine per incoming message — simple concurrency model
- CLI channel's `Start()` is blocking (it's the REPL loop) — other channels would run in background goroutines
- Session tape file uses timestamp in name — easy to find and review later
- Working directory is `os.Getwd()` — the agent operates in whatever directory golem is started from

## Done When

- `make build` produces `bin/golem`
- `./bin/golem` shows startup banner, enters REPL
- Ask a question → get LLM response
- `,help` → shows commands
- `,quit` → exits cleanly
- Ctrl+C → exits cleanly with "Goodbye"
- `./bin/golem --model anthropic:claude-sonnet-4-20250514` → uses Anthropic
- Tape file is created and grows with each interaction
