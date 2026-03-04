package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/joechenrh/golem/internal/agent"
	"github.com/joechenrh/golem/internal/channel"
	"github.com/joechenrh/golem/internal/channel/cli"
	"github.com/joechenrh/golem/internal/config"
	"github.com/joechenrh/golem/internal/ctxmgr"
	"github.com/joechenrh/golem/internal/executor"
	"github.com/joechenrh/golem/internal/fs"
	"github.com/joechenrh/golem/internal/hooks"
	"github.com/joechenrh/golem/internal/llm"
	"github.com/joechenrh/golem/internal/tape"
	"github.com/joechenrh/golem/internal/tools"
	"github.com/joechenrh/golem/internal/tools/builtin"
)

const version = "0.1.0"

func main() {
	// 1. Parse CLI flags.
	flags := parseFlags()
	if flags == nil {
		return // --help or --version handled
	}

	// 2. Load config.
	cfg, err := config.Load(flags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	// 3. Initialize logger.
	logger := initLogger(cfg.LogLevel)
	defer logger.Sync()

	// 4. Initialize LLM client.
	provider, modelName := llm.ParseModelProvider(cfg.Model)
	apiKey := cfg.APIKeys[string(provider)]
	if apiKey == "" {
		fmt.Fprintf(os.Stderr, "no API key for provider %q. Set %s_API_KEY environment variable.\n",
			provider, toEnvPrefix(string(provider)))
		os.Exit(1)
	}

	var clientOpts []llm.ClientOption
	if baseURL := cfg.BaseURLs[string(provider)]; baseURL != "" {
		clientOpts = append(clientOpts, llm.WithBaseURL(baseURL))
	}

	llmClient, err := llm.NewClient(provider, apiKey, clientOpts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "LLM client error: %v\n", err)
		os.Exit(1)
	}
	_ = modelName // used by agent via cfg.Model

	// 5. Initialize tape store.
	if err := os.MkdirAll(cfg.TapeDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "creating tape dir: %v\n", err)
		os.Exit(1)
	}
	tapePath := filepath.Join(cfg.TapeDir, fmt.Sprintf("session-%s.jsonl", time.Now().Format("20060102-150405")))
	tapeStore, err := tape.NewFileStore(tapePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tape store error: %v\n", err)
		os.Exit(1)
	}

	// 6. Initialize executor and filesystem.
	workDir, _ := os.Getwd()

	var exec executor.Executor
	switch cfg.Executor {
	case "noop":
		exec = executor.NewNoop()
	default:
		exec = executor.NewLocal(workDir)
	}

	filesystem, err := fs.NewLocalFS(workDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "filesystem error: %v\n", err)
		os.Exit(1)
	}

	// 7. Initialize context strategy.
	ctxStrategy, err := ctxmgr.NewContextStrategy(cfg.ContextStrategy)
	if err != nil {
		fmt.Fprintf(os.Stderr, "context strategy error: %v\n", err)
		os.Exit(1)
	}

	// 8. Build hook bus.
	hookBus := hooks.NewBus(logger)
	hookBus.Register(hooks.NewLoggingHook(logger))

	// 9. Build tool registry.
	registry := tools.NewRegistry()
	registry.RegisterAll(
		builtin.NewShellTool(exec, cfg.ShellTimeout),
		builtin.NewReadFileTool(filesystem),
		builtin.NewWriteFileTool(filesystem),
		builtin.NewEditFileTool(filesystem),
		builtin.NewListDirectoryTool(filesystem),
		builtin.NewSearchFilesTool(filesystem),
	)
	// Register stubs for future features.
	for _, s := range builtin.WebStubs() {
		s := s
		registry.Register(&s)
	}

	// Discover skills from the skills directory.
	if err := registry.DiscoverSkills(cfg.SkillsDir); err != nil {
		logger.Debug("skills discovery", zap.Error(err))
	}

	// 10. Create agent loop.
	agentLoop := agent.New(llmClient, registry, tapeStore, ctxStrategy, hookBus, cfg, logger)

	// 11. Setup signal handling.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// 12. Create CLI channel and run.
	cliCh := cli.New()
	cliCh.PrintBanner(cfg.Model, len(registry.ToolDefinitions()), tapePath)

	inCh := make(chan channel.IncomingMessage, 100)

	// Message processing goroutine.
	go func() {
		for msg := range inCh {
			processMessage(ctx, agentLoop, cliCh, msg, logger)
		}
	}()

	// Start REPL (blocks until EOF or context cancel).
	if err := cliCh.Start(ctx, inCh); err != nil && err != context.Canceled {
		logger.Error("CLI channel error", zap.Error(err))
	}

	close(inCh)
	cliCh.PrintSystem("Goodbye!")
}

// processMessage handles a single incoming message through the agent loop.
func processMessage(ctx context.Context, agentLoop *agent.AgentLoop, cliCh *cli.CLIChannel, msg channel.IncomingMessage, logger *zap.Logger) {
	if cliCh.SupportsStreaming() {
		tokenCh := make(chan string, 100)

		go func() {
			if err := cliCh.SendStream(ctx, msg.ChannelID, tokenCh); err != nil {
				logger.Error("stream send error", zap.Error(err))
			}
		}()

		if err := agentLoop.HandleInputStream(ctx, msg, tokenCh); err != nil {
			if err == agent.ErrQuit {
				cliCh.PrintSystem("Goodbye!")
				os.Exit(0)
			}
			cliCh.PrintError("Error: " + err.Error())
		}
	} else {
		response, err := agentLoop.HandleInput(ctx, msg)
		if err != nil {
			if err == agent.ErrQuit {
				cliCh.PrintSystem("Goodbye!")
				os.Exit(0)
			}
			cliCh.PrintError("Error: " + err.Error())
			return
		}
		if err := cliCh.Send(ctx, channel.OutgoingMessage{ChannelID: msg.ChannelID, Text: response}); err != nil {
			logger.Error("send error", zap.Error(err))
		}
	}
}

// parseFlags parses CLI flags and returns overrides for config.Load.
// Returns nil if --help or --version was requested.
func parseFlags() map[string]string {
	model := flag.String("model", "", "LLM model (e.g. \"openai:gpt-4o\") (env: GOLEM_MODEL)")
	tapeDir := flag.String("tape-dir", "", "Directory for tape files (env: GOLEM_TAPE_DIR)")
	skillsDir := flag.String("skills-dir", "", "Skills discovery directory (env: GOLEM_SKILLS_DIR)")
	logLevel := flag.String("log-level", "", "Log level: debug, info, warn, error (env: GOLEM_LOG_LEVEL)")
	showVersion := flag.Bool("version", false, "Show version")

	flag.Parse()

	if *showVersion {
		fmt.Printf("golem v%s\n", version)
		return nil
	}

	overrides := make(map[string]string)
	if *model != "" {
		overrides["model"] = *model
	}
	if *tapeDir != "" {
		overrides["tape-dir"] = *tapeDir
	}
	if *skillsDir != "" {
		overrides["skills-dir"] = *skillsDir
	}
	if *logLevel != "" {
		overrides["log-level"] = *logLevel
	}
	return overrides
}

// initLogger creates a zap.Logger based on the level string.
func initLogger(level string) *zap.Logger {
	var zapLevel zapcore.Level
	switch level {
	case "debug":
		zapLevel = zapcore.DebugLevel
	case "warn":
		zapLevel = zapcore.WarnLevel
	case "error":
		zapLevel = zapcore.ErrorLevel
	default:
		zapLevel = zapcore.InfoLevel
	}

	cfg := zap.Config{
		Level:            zap.NewAtomicLevelAt(zapLevel),
		Encoding:         "console",
		EncoderConfig:    zap.NewDevelopmentEncoderConfig(),
		OutputPaths:      []string{"stderr"},
		ErrorOutputPaths: []string{"stderr"},
	}
	cfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	logger, err := cfg.Build()
	if err != nil {
		// Fallback to nop if config fails.
		return zap.NewNop()
	}
	return logger
}

// toEnvPrefix converts a provider name to an env var prefix (uppercase).
func toEnvPrefix(s string) string {
	result := make([]byte, len(s))
	for i := range s {
		if s[i] >= 'a' && s[i] <= 'z' {
			result[i] = s[i] - 32
		} else {
			result[i] = s[i]
		}
	}
	return string(result)
}
