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
	"syscall"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/sync/errgroup"

	"github.com/joechenrh/golem/internal/channel/cli"
	"github.com/joechenrh/golem/internal/config"
)

const version = "0.1.0"

func main() {
	// 1. Parse CLI flags.
	flags := parseFlags()
	if flags == nil {
		return // --help or --version handled
	}

	// 2. Load config for the default (CLI) agent.
	cfg, err := config.Load("default", flags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	// 3. Initialize logger.
	logger := initLogger(cfg.LogLevel, cfg.TapeDir)
	defer logger.Sync()

	// 4. Build the default (interactive) agent.
	defaultAgent, err := buildAgent("default", cfg, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent init error: %v\n", err)
		os.Exit(1)
	}

	// 5. Print banner before entering the run loop.
	cliCh := defaultAgent.Channels["cli"].(*cli.CLIChannel)
	cliCh.PrintBanner(cfg.Model, len(defaultAgent.Registry.ToolDefinitions()), defaultAgent.TapePath)

	// 6. Setup signal handling.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// 7. Run agents with a plain errgroup (no WithContext).
	// The CLI agent's goroutine calls defer cancel() so that when it exits
	// for any reason, the shared context is cancelled and all background
	// agents stop.
	g := new(errgroup.Group)

	// CLI agent: when it exits, cancel everything.
	g.Go(func() error {
		defer cancel()
		return defaultAgent.Run(ctx)
	})

	// Background agents: errors logged, don't kill CLI.
	for _, ba := range discoverAndBuildBackgroundAgents(cliCh, logger) {
		g.Go(func() error {
			if err := ba.Run(ctx); err != nil && !errors.Is(err, ErrAgentQuit) {
				logger.Error("background agent error", zap.String("agent", ba.Name), zap.Error(err))
			}
			return nil
		})
	}

	g.Wait()
	cliCh.PrintSystem("Goodbye!")
}

// discoverAndBuildBackgroundAgents finds agent configs in ~/.golem/agents/,
// loads each one, and builds an AgentInstance for those with remote channels.
func discoverAndBuildBackgroundAgents(cliCh *cli.CLIChannel, logger *zap.Logger) []*AgentInstance {
	names, err := config.DiscoverAgents()
	if err != nil {
		logger.Error("agent discovery", zap.Error(err))
		return nil
	}

	var agents []*AgentInstance
	for _, name := range names {
		if name == "default" {
			continue // already used by the CLI agent
		}

		agentCfg, err := config.Load(name, nil)
		if err != nil {
			logger.Error("loading agent config", zap.String("agent", name), zap.Error(err))
			continue
		}

		if !agentCfg.HasRemoteChannels() {
			logger.Debug("skipping agent without remote channels", zap.String("agent", name))
			continue
		}

		inst, err := buildAgent(name, agentCfg, logger.Named(name))
		if err != nil {
			logger.Error("building agent", zap.String("agent", name), zap.Error(err))
			continue
		}

		cliCh.PrintSystem(fmt.Sprintf("Background agent %q started", name))
		agents = append(agents, inst)
	}
	return agents
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
