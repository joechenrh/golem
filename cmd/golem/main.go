package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/joechenrh/golem/internal/agent"
	"github.com/joechenrh/golem/internal/channel"
	"github.com/joechenrh/golem/internal/channel/cli"
	larkchan "github.com/joechenrh/golem/internal/channel/lark"
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
	logger := initLogger(cfg.LogLevel, cfg.TapeDir)
	defer logger.Sync()

	// 4. Initialize LLM client.
	provider, modelName := llm.ParseModelProvider(cfg.Model)
	apiKey := cfg.APIKeys[string(provider)]
	if apiKey == "" {
		fmt.Fprintf(os.Stderr, "no API key for provider %q. Set %s_API_KEY environment variable.\n",
			provider, strings.ToUpper(string(provider)))
		os.Exit(1)
	}

	// Auto-register unknown providers as OpenAI-compatible.
	if provider != llm.ProviderOpenAI && provider != llm.ProviderAnthropic {
		baseURL := cfg.BaseURLs[string(provider)]
		if baseURL == "" {
			fmt.Fprintf(os.Stderr, "custom provider %q requires %s_BASE_URL to be set.\n",
				provider, strings.ToUpper(string(provider)))
			os.Exit(1)
		}
		llm.RegisterProvider(provider, baseURL, llm.NewOpenAICompatibleClient)
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

	// 9. Build channel registry (before tools so Lark tools can reference the channel).
	cliCh := cli.New()
	channels := map[string]channel.Channel{"cli": cliCh}

	var larkCh *larkchan.LarkChannel
	if cfg.LarkAppID != "" && cfg.LarkAppSecret != "" {
		larkCh = larkchan.New(cfg.LarkAppID, cfg.LarkAppSecret, cfg.LarkVerifyToken, logger)
		channels["lark"] = larkCh
	}

	// 10. Build tool registry.
	registry := tools.NewRegistry()
	registry.RegisterAll(
		builtin.NewShellTool(exec, cfg.ShellTimeout),
		builtin.NewReadFileTool(filesystem),
		builtin.NewWriteFileTool(filesystem),
		builtin.NewEditFileTool(filesystem),
		builtin.NewListDirectoryTool(filesystem),
		builtin.NewSearchFilesTool(filesystem),
	)
	// Register web tools.
	webClient := &http.Client{Timeout: 30 * time.Second}
	registry.RegisterAll(
		builtin.NewWebSearchTool(webClient),
		builtin.NewWebFetchTool(webClient),
	)
	// Register Lark tools if channel is enabled (pre-expanded so the LLM
	// sees full parameter schemas immediately).
	if larkCh != nil {
		registry.RegisterAll(
			builtin.NewLarkSendTool(larkCh),
			builtin.NewLarkListChatsTool(larkCh),
		)
		registry.Expand("lark_send")
		registry.Expand("lark_list_chats")
	}

	// Discover skills from the skills directory.
	if err := registry.DiscoverSkills(cfg.SkillsDir); err != nil {
		logger.Debug("skills discovery", zap.Error(err))
	}

	// 11. Create agent loop.
	agentLoop := agent.New(llmClient, registry, tapeStore, ctxStrategy, hookBus, cfg, logger)

	// 12. Setup signal handling.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cliCh.PrintBanner(cfg.Model, len(registry.ToolDefinitions()), tapePath)

	inCh := make(chan channel.IncomingMessage, 100)

	// Message processing goroutine.
	go func() {
		for msg := range inCh {
			if processMessage(ctx, agentLoop, channels, msg, logger) {
				cancel() // signal the REPL to stop
				return
			}
		}
	}()

	// Start Lark channel in background if configured.
	if larkCh != nil {
		cliCh.PrintSystem("Lark channel enabled (WebSocket mode)")
		go func() {
			if err := larkCh.Start(ctx, inCh); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("Lark channel error", zap.Error(err))
			}
		}()
	}

	// Start REPL (blocks until EOF or context cancel).
	if err := cliCh.Start(ctx, inCh); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("CLI channel error", zap.Error(err))
	}

	close(inCh)
	cliCh.PrintSystem("Goodbye!")
}

// processMessage handles a single incoming message through the agent loop.
// Returns true if the user requested quit.
func processMessage(ctx context.Context, agentLoop *agent.AgentLoop, channels map[string]channel.Channel, msg channel.IncomingMessage, logger *zap.Logger) bool {
	if msg.Done != nil {
		defer close(msg.Done)
	}

	ch, ok := channels[msg.ChannelName]
	if !ok {
		logger.Error("unknown channel", zap.String("channel", msg.ChannelName))
		return false
	}

	if ch.SupportsStreaming() {
		tokenCh := make(chan string, 100)

		streamDone := make(chan struct{})
		go func() {
			defer close(streamDone)
			if err := ch.SendStream(ctx, msg.ChannelID, tokenCh); err != nil {
				logger.Error("stream send error", zap.Error(err))
			}
		}()

		err := agentLoop.HandleInputStream(ctx, msg, tokenCh)
		close(tokenCh)
		<-streamDone // wait for SendStream to finish
		if err != nil {
			if errors.Is(err, agent.ErrQuit) {
				return true
			}
			logOrPrintError(ch, logger, "Error: "+err.Error())
		}
	} else {
		response, err := agentLoop.HandleInput(ctx, msg)
		if err != nil {
			if errors.Is(err, agent.ErrQuit) {
				return true
			}
			logOrPrintError(ch, logger, "Error: "+err.Error())
			return false
		}
		if err := ch.Send(ctx, channel.OutgoingMessage{ChannelID: msg.ChannelID, Text: response}); err != nil {
			logger.Error("send error", zap.Error(err))
		}
	}
	return false
}

// logOrPrintError uses CLIChannel.PrintError for colored output when available,
// otherwise logs the error.
func logOrPrintError(ch channel.Channel, logger *zap.Logger, text string) {
	if cliCh, ok := ch.(*cli.CLIChannel); ok {
		cliCh.PrintError(text)
	} else {
		logger.Error(text)
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

// initLogger creates a zap.Logger that writes to a file in logDir.
// This keeps log output from mixing with the interactive REPL.
func initLogger(level, logDir string) *zap.Logger {
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

	logFile := filepath.Join(logDir, fmt.Sprintf("golem-%s.log", time.Now().Format("20060102-150405")))

	cfg := zap.Config{
		Level:            zap.NewAtomicLevelAt(zapLevel),
		Encoding:         "console",
		EncoderConfig:    zap.NewDevelopmentEncoderConfig(),
		OutputPaths:      []string{logFile},
		ErrorOutputPaths: []string{logFile},
	}
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	logger, err := cfg.Build()
	if err != nil {
		// Fallback to nop if config fails.
		return zap.NewNop()
	}
	return logger
}
