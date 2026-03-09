// Package main is the entry point for golem, an AI agent framework implementing
// the ReAct loop with pluggable LLM providers, communication channels, and tools.
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
	"golang.org/x/sync/errgroup"

	"github.com/joechenrh/golem/internal/app"
	"github.com/joechenrh/golem/internal/config"
	"github.com/joechenrh/golem/internal/metrics"
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
	logger, err := initLogger(cfg.LogLevel, cfg.TapeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logger init error: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	// 4. Build the default (interactive) agent.
	defaultAgent, err := app.BuildAgent("default", cfg, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent init error: %v\n", err)
		os.Exit(1)
	}

	// 5. Start metrics server if configured.
	var collector *metrics.Collector
	if cfg.MetricsPort != "" {
		collector = metrics.NewCollector()
		collector.RegisterAgent("default", defaultAgent.MetricsHook)
		if defaultAgent.Sessions != nil {
			collector.RegisterSessions("default", defaultAgent.Sessions)
		}

		mux := http.NewServeMux()
		mux.Handle("/debug/metrics", metrics.NewHandler(collector))
		mux.Handle("/debug/metrics.json", metrics.NewJSONHandler(collector))
		mux.Handle("/debug/dashboard", metrics.NewDashboardHandler())
		go func() {
			addr := ":" + cfg.MetricsPort
			logger.Info("metrics server starting", zap.String("addr", addr))
			if err := http.ListenAndServe(addr, mux); err != nil {
				logger.Error("metrics server error", zap.Error(err))
			}
		}()
	}

	// 6. Print banner before entering the run loop.
	defaultAgent.Printer.PrintBanner(cfg.Model, len(defaultAgent.Registry.ToolDefinitions()), defaultAgent.TapePath)

	// 7. Setup signal handling.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// 7. Track claimed Lark app IDs to avoid duplicate WebSocket connections.
	claimedLarkApps := make(map[string]bool)
	if defaultAgent.Config.LarkAppID != "" {
		claimedLarkApps[defaultAgent.Config.LarkAppID] = true
	}

	// 8. Run agents with a plain errgroup (no WithContext).
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
	for _, ba := range app.DiscoverAndBuildBackgroundAgents(defaultAgent.Printer, logger, claimedLarkApps) {
		if collector != nil {
			collector.RegisterAgent(ba.Name, ba.MetricsHook)
			if ba.Sessions != nil {
				collector.RegisterSessions(ba.Name, ba.Sessions)
			}
		}
		g.Go(func() error {
			if err := ba.Run(ctx); err != nil && !errors.Is(err, app.ErrAgentQuit) {
				logger.Error("background agent error", zap.String("agent", ba.Name), zap.Error(err))
			}
			return nil
		})
	}

	g.Wait()
	defaultAgent.Printer.PrintSystem("Goodbye!")
}

// parseFlags parses CLI flags and returns overrides for config.Load.
// Returns nil if --help or --version was requested.
func parseFlags() map[string]string {
	model := flag.String("model", "", "LLM model (e.g. \"openai:gpt-4o\") (env: GOLEM_MODEL)")
	tapeDir := flag.String("tape-dir", "", "Directory for tape files (env: GOLEM_TAPE_DIR)")
	workspaceDir := flag.String("workspace-dir", "", "Agent workspace directory (env: GOLEM_WORKSPACE_DIR)")
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
	if *workspaceDir != "" {
		overrides["workspace-dir"] = *workspaceDir
	}
	if *logLevel != "" {
		overrides["log-level"] = *logLevel
	}
	return overrides
}

// initLogger creates a zap.Logger that writes to a file in logDir.
// This keeps log output from mixing with the interactive REPL.
// It also creates a golem-latest.log symlink and cleans up old logs.
func initLogger(level, logDir string) (*zap.Logger, error) {
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

	// Ensure the log directory exists (e.g. after a tape dir reset).
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("create log dir %s: %w", logDir, err)
	}

	logFile := filepath.Join(logDir, fmt.Sprintf("golem-%s.log", time.Now().Format("20060102-150405")))

	cfg := zap.Config{
		Level:    zap.NewAtomicLevelAt(zapLevel),
		Encoding: "console",
		EncoderConfig: zapcore.EncoderConfig{
			TimeKey:          "T",
			LevelKey:         "L",
			NameKey:          "N",
			CallerKey:        "C",
			MessageKey:       "M",
			StacktraceKey:    "S",
			LineEnding:       zapcore.DefaultLineEnding,
			EncodeLevel:      bracketLevelEncoder,
			EncodeTime:       bracketTimeEncoder,
			EncodeName:       bracketNameEncoder,
			EncodeCaller:     bracketCallerEncoder,
			ConsoleSeparator: " ",
		},
		OutputPaths:      []string{logFile},
		ErrorOutputPaths: []string{logFile},
	}

	logger, err := cfg.Build()
	if err != nil {
		return nil, err
	}

	// Create/update golem-latest.log symlink.
	symlink := filepath.Join(logDir, "golem-latest.log")
	os.Remove(symlink)
	os.Symlink(filepath.Base(logFile), symlink)

	// Clean up log files older than 7 days.
	cleanOldLogs(logDir, 7*24*time.Hour)

	return logger.Named("golem"), nil
}

func cleanOldLogs(dir string, maxAge time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if e.IsDir() || e.Name() == "golem-latest.log" {
			continue
		}
		if !strings.HasPrefix(e.Name(), "golem-") || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func bracketTimeEncoder(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString("[" + t.Format("2006-01-02 15:04:05.000") + "]")
}

func bracketLevelEncoder(l zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString("[" + l.CapitalString() + "]")
}

func bracketNameEncoder(name string, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString("[" + name + "]")
}

func bracketCallerEncoder(caller zapcore.EntryCaller, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(fmt.Sprintf("[%s:%d]", filepath.Base(caller.File), caller.Line))
}
