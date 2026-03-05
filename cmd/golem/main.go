// Package main is the entry point for golem, an AI agent framework implementing
// the ReAct loop with pluggable LLM providers, communication channels, and tools.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/joechenrh/golem/internal/agent"
	"github.com/joechenrh/golem/internal/channel"
	"github.com/joechenrh/golem/internal/channel/cli"
	larkchan "github.com/joechenrh/golem/internal/channel/lark"
	"github.com/joechenrh/golem/internal/config"
)

const version = "0.1.0"

func main() {
	// 1. Parse CLI flags.
	flags := parseFlags()
	if flags == nil {
		return // --help or --version handled
	}

	// 2. Load config.
	cfg, err := config.Load("", flags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	// 3. Initialize logger.
	logger := initLogger(cfg.LogLevel, cfg.TapeDir)
	defer logger.Sync()

	// 4. Build the agent instance (LLM client, tape, tools, channels).
	inst, err := buildAgent(cfg, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent init error: %v\n", err)
		os.Exit(1)
	}

	// 5. Setup signal handling.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cliCh := inst.Channels["cli"].(*cli.CLIChannel)
	cliCh.PrintBanner(cfg.Model, len(inst.Registry.ToolDefinitions()), inst.TapePath)

	inCh := make(chan channel.IncomingMessage, 100)

	var wg sync.WaitGroup

	// Message processing goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for msg := range inCh {
			if processMessage(ctx, inst.Loop, inst.Channels, msg, logger) {
				cancel()
				return
			}
		}
	}()

	// Start Lark channel in background if configured.
	if larkCh, ok := inst.Channels["lark"].(*larkchan.LarkChannel); ok {
		cliCh.PrintSystem("Lark channel enabled (WebSocket mode)")
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := larkCh.Start(ctx, inCh); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("Lark channel error", zap.Error(err))
			}
		}()
	}

	// Start REPL (blocks until EOF or context cancel).
	if err := cliCh.Start(ctx, inCh); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("CLI channel error", zap.Error(err))
	}

	cancel()
	close(inCh)
	wg.Wait()
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
		<-streamDone
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
		return zap.NewNop()
	}
	return logger
}
