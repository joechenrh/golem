// Package app wires together agent components and manages the runtime lifecycle.
// It is separate from package main so the wiring logic can be tested and reused.
package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

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
	"github.com/joechenrh/golem/internal/memory"
	"github.com/joechenrh/golem/internal/redact"
	"github.com/joechenrh/golem/internal/tape"
	"github.com/joechenrh/golem/internal/tools"
	"github.com/joechenrh/golem/internal/tools/builtin"
)

// ErrAgentQuit is returned by Run when the user requests quit via the CLI.
var ErrAgentQuit = errors.New("agent quit")

// AgentInstance bundles a running agent with its channels and metadata.
type AgentInstance struct {
	Name     string
	Config   *config.Config
	Logger   *zap.Logger
	Loop     *agent.AgentLoop      // used by CLI (single session)
	Sessions *agent.SessionManager // used by remote channels (per-chat)
	Channels map[string]channel.Channel
	Printer  channel.SystemPrinter // formatted output (e.g. CLI channel)
	Registry *tools.Registry
	TapePath string
}

// Run starts all channels, processes incoming messages, and blocks until the
// agent is done. It uses an explicit cancel and two errgroups:
//   - An inner plain errgroup tracks channel goroutines; when all channels
//     exit, inCh is closed so the message processor drains and returns.
//   - An outer plain errgroup ties the message processor and the channel
//     closer together.
//
// gcancel is called when any channel exits (e.g. CLI EOF) or when the user
// types /quit, ensuring all remaining channels stop promptly.
func (inst *AgentInstance) Run(ctx context.Context) error {
	inCh := make(chan channel.IncomingMessage, 100)
	gctx, gcancel := context.WithCancel(ctx)
	defer gcancel()

	// Start periodic session eviction if sessions are enabled.
	if inst.Sessions != nil {
		inst.Sessions.SetBaseContext(gctx)
		inst.Sessions.StartEvictionLoop(gctx,
			10*time.Minute, inst.Config.SessionIdleTime)
		defer inst.Sessions.Shutdown()
	}

	var g errgroup.Group

	// Inner errgroup: tracks when all channel writers finish.
	var cg errgroup.Group
	for _, ch := range inst.Channels {
		cg.Go(func() error {
			err := ch.Start(gctx, inCh)
			// Any channel exiting should stop all others (e.g. CLI EOF).
			gcancel()
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		})
	}

	// When all channels exit, close inCh so the message processor drains.
	g.Go(func() error {
		err := cg.Wait()
		close(inCh)
		return err
	})

	g.Go(func() error {
		return inst.processMessages(gctx, gcancel, inCh)
	})

	return g.Wait()
}

// processMessages reads from inCh and dispatches messages. CLI messages are
// handled inline; remote messages are fanned out to per-channelID workers so
// different chats run in parallel while messages within a chat stay serialized.
func (inst *AgentInstance) processMessages(
	ctx context.Context,
	cancel context.CancelFunc,
	inCh <-chan channel.IncomingMessage,
) error {
	chatQueues := make(map[string]chan channel.IncomingMessage)
	var chatGroup errgroup.Group
	defer func() {
		for _, q := range chatQueues {
			close(q)
		}
		chatGroup.Wait()
	}()

	for msg := range inCh {
		if msg.ChannelName == "cli" {
			if inst.processMessage(ctx, msg) {
				cancel()
				return ErrAgentQuit
			}
			continue
		}

		q, ok := chatQueues[msg.ChannelID]
		if !ok {
			q = make(chan channel.IncomingMessage, 16)
			chatQueues[msg.ChannelID] = q
			chatGroup.Go(func() error {
				for m := range q {
					if inst.processMessage(ctx, m) {
						cancel()
					}
				}
				return nil
			})
		}
		q <- msg
	}
	return nil
}

// processMessage handles a single incoming message through the agent loop.
// Returns true if the user requested quit.
func (inst *AgentInstance) processMessage(
	ctx context.Context, msg channel.IncomingMessage,
) bool {
	if msg.Done != nil {
		defer close(msg.Done)
	}

	ch, ok := inst.Channels[msg.ChannelName]
	if !ok {
		inst.Logger.Error("unknown channel", zap.String("channel", msg.ChannelName))
		return false
	}

	// Select the appropriate AgentLoop: per-chat session for remote channels,
	// the shared loop for CLI. For sessions, use the session's context so that
	// eviction cancels in-flight work.
	loop := inst.Loop
	loopCtx := ctx
	if inst.Sessions != nil && msg.ChannelName != "cli" {
		loop, loopCtx = inst.Sessions.GetOrCreate(msg.ChannelID)
	}

	if ch.SupportsStreaming() {
		tokenCh := make(chan string, 100)

		streamDone := make(chan struct{})
		go func() {
			defer close(streamDone)
			if err := ch.SendStream(loopCtx, msg.ChannelID, tokenCh); err != nil {
				inst.Logger.Error("stream send error", zap.Error(err))
			}
		}()

		err := loop.HandleInputStream(loopCtx, msg, tokenCh)
		close(tokenCh)
		<-streamDone
		if err != nil {
			if errors.Is(err, agent.ErrQuit) {
				return true
			}
			inst.logOrPrintError(ch, "Error: "+err.Error())
		}
	} else {
		response, err := loop.HandleInput(loopCtx, msg)
		if err != nil {
			if errors.Is(err, agent.ErrQuit) {
				return true
			}
			inst.logOrPrintError(ch, "Error: "+err.Error())
			return false
		}
		if err := ch.Send(loopCtx, channel.OutgoingMessage{ChannelID: msg.ChannelID, Text: response}); err != nil {
			inst.Logger.Error("send error", zap.Error(err))
		}
	}
	return false
}

// logOrPrintError uses PrintError for colored output when the channel supports
// it, otherwise logs the error.
func (inst *AgentInstance) logOrPrintError(
	ch channel.Channel, text string,
) {
	if p, ok := ch.(interface{ PrintError(string) }); ok {
		p.PrintError(text)
	} else {
		inst.Logger.Error(text)
	}
}

// BuildAgent creates a fully wired AgentInstance from config.
// When name is "default", a CLI channel is created for interactive use.
// Otherwise, no CLI channel is added (background agent).
// Lark/Telegram channels are always conditional on config credentials.
func BuildAgent(
	name string, cfg *config.Config, logger *zap.Logger,
) (*AgentInstance, error) {
	// 1. Initialize LLM client.
	llmClient, err := BuildLLMClient(cfg)
	if err != nil {
		return nil, err
	}

	// 2. Initialize tape store.
	if err := os.MkdirAll(cfg.TapeDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating tape dir: %w", err)
	}
	tapePath := filepath.Join(cfg.TapeDir, fmt.Sprintf("session-%s-%s.jsonl", name, time.Now().Format("20060102-150405")))
	tapeStore, err := tape.NewFileStore(tapePath)
	if err != nil {
		return nil, fmt.Errorf("tape store: %w", err)
	}

	// 3. Initialize executor and filesystem.
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
		return nil, fmt.Errorf("filesystem: %w", err)
	}

	// 4. Initialize context strategy.
	ctxStrategy, err := ctxmgr.NewContextStrategy(cfg.ContextStrategy)
	if err != nil {
		return nil, fmt.Errorf("context strategy: %w", err)
	}

	// 5. Build hook bus.
	hookBus := hooks.NewBus(logger)
	hookBus.Register(hooks.NewLoggingHook(logger))

	// 6. Build channel registry.
	channels := make(map[string]channel.Channel)

	// CLI channel only for the default (interactive) agent.
	var printer channel.SystemPrinter
	if name == "default" {
		cliCh := cli.New()
		channels["cli"] = cliCh
		printer = cliCh
	}

	var larkCh *larkchan.LarkChannel
	if cfg.LarkAppID != "" && cfg.LarkAppSecret != "" {
		larkCh = larkchan.New(cfg.LarkAppID, cfg.LarkAppSecret, cfg.LarkVerifyToken, logger)
		channels["lark"] = larkCh
	}

	// 7. Build tool registry.
	registry := BuildToolRegistry(cfg, exec, filesystem, larkCh, logger)

	// 8. Register spawn_agent tool. The runner creates a sub-agent that
	//    shares the LLM client and infra but has its own tape and registry
	//    WITHOUT spawn capability (prevents recursive spawning).
	var subAgentSeq atomic.Int64
	runner := func(ctx context.Context, prompt string) (string, error) {
		seq := subAgentSeq.Add(1)
		subTapePath := filepath.Join(cfg.TapeDir, fmt.Sprintf("sub-%d-%s.jsonl", seq, time.Now().Format("20060102-150405")))
		subTape, err := tape.NewFileStore(subTapePath)
		if err != nil {
			return "", fmt.Errorf("sub-agent tape: %w", err)
		}
		subCtxStrategy, _ := ctxmgr.NewContextStrategy(cfg.ContextStrategy)
		subHooks := hooks.NewBus(logger.Named("sub-agent"))
		subHooks.Register(hooks.NewLoggingHook(logger.Named("sub-agent")))

		// Build a registry without spawn_agent.
		subRegistry := BuildToolRegistry(cfg, exec, filesystem, larkCh, logger)

		subLoop := agent.New(llmClient, subRegistry, subTape, subCtxStrategy, subHooks, cfg, logger.Named("sub-agent"))

		msg := channel.IncomingMessage{
			ChannelName: "internal",
			Text:        prompt,
		}
		return subLoop.HandleInput(ctx, msg)
	}
	registry.Register(builtin.NewSpawnAgentTool(runner))

	// 9. Create agent loop.
	loop := agent.New(llmClient, registry, tapeStore, ctxStrategy, hookBus, cfg, logger)

	// 10. Create SessionManager for agents with remote channels.
	// Each remote chat gets its own AgentLoop with isolated tape and tools.
	var sessions *agent.SessionManager
	if cfg.HasRemoteChannels() {
		toolFactory := func() *tools.Registry {
			return BuildToolRegistry(cfg, exec, filesystem, larkCh, logger)
		}
		sessions = agent.NewSessionManager(agent.SessionFactory{
			LLMClient:       llmClient,
			Config:          cfg,
			Logger:          logger,
			ToolFactory:     toolFactory,
			ContextStrategy: cfg.ContextStrategy,
			AgentName:       name,
		}, logger)

		// Restore sessions from existing tape files.
		if err := sessions.LoadExisting(cfg.TapeDir); err != nil {
			logger.Warn("failed to restore sessions", zap.Error(err))
		}
	}

	return &AgentInstance{
		Name:     name,
		Config:   cfg,
		Logger:   logger,
		Loop:     loop,
		Sessions: sessions,
		Channels: channels,
		Printer:  printer,
		Registry: registry,
		TapePath: tapePath,
	}, nil
}

// BuildLLMClient creates an LLM client from the config, auto-registering
// unknown providers as OpenAI-compatible.
func BuildLLMClient(cfg *config.Config) (llm.Client, error) {
	provider, _ := llm.ParseModelProvider(cfg.Model)
	apiKey := cfg.APIKeys[string(provider)]
	if apiKey == "" {
		return nil, fmt.Errorf("no API key for provider %q — set %s_API_KEY",
			provider, strings.ToUpper(string(provider)))
	}

	// Auto-register unknown providers as OpenAI-compatible.
	if provider != llm.ProviderOpenAI && provider != llm.ProviderAnthropic {
		baseURL := cfg.BaseURLs[string(provider)]
		if baseURL == "" {
			return nil, fmt.Errorf("custom provider %q requires %s_BASE_URL",
				provider, strings.ToUpper(string(provider)))
		}
		llm.RegisterProvider(provider, baseURL, llm.NewOpenAICompatibleClient)
	}

	var opts []llm.ClientOption
	if baseURL := cfg.BaseURLs[string(provider)]; baseURL != "" {
		opts = append(opts, llm.WithBaseURL(baseURL))
	}
	client, err := llm.NewClient(provider, apiKey, opts...)
	if err != nil {
		return nil, err
	}
	return llm.NewRateLimitedClient(client, cfg.LLMRateLimit), nil
}

// BuildToolRegistry creates and populates a tool registry with all built-in
// tools, channel-specific tools, and discovered skills.
// The registry intentionally does NOT include spawn_agent — that is added
// separately by BuildAgent to prevent sub-agents from spawning recursively.
func BuildToolRegistry(
	cfg *config.Config,
	exec executor.Executor,
	filesystem *fs.LocalFS,
	larkCh *larkchan.LarkChannel,
	logger *zap.Logger,
) *tools.Registry {
	registry := tools.NewRegistry()

	// Core tools.
	registry.RegisterAll(
		builtin.NewShellTool(exec, cfg.ShellTimeout),
		builtin.NewReadFileTool(filesystem),
		builtin.NewWriteFileTool(filesystem),
		builtin.NewEditFileTool(filesystem),
		builtin.NewListDirectoryTool(filesystem),
		builtin.NewSearchFilesTool(filesystem),
	)

	// Web tools.
	webClient := &http.Client{Timeout: 30 * time.Second}
	registry.RegisterAll(
		builtin.NewWebSearchTool(webClient, cfg.WebSearchBackend),
		builtin.NewWebFetchTool(webClient),
	)

	// Lark tools (pre-expanded for immediate full schema).
	if larkCh != nil {
		registry.RegisterAll(
			builtin.NewLarkSendTool(larkCh),
			builtin.NewLarkListChatsTool(larkCh),
			builtin.NewLarkReadDocTool(larkCh),
			builtin.NewLarkWriteDocTool(larkCh),
		)
		registry.Expand("lark_send")
		registry.Expand("lark_list_chats")
		registry.Expand("lark_read_doc")
		registry.Expand("lark_write_doc")
	}

	// Memory tools (mnemos direct mode).
	if cfg.MnemosDBHost != "" {
		mnemosClient := memory.NewClient(
			&http.Client{Timeout: 30 * time.Second},
			cfg.MnemosDBHost, cfg.MnemosDBUser, cfg.MnemosDBPass,
			cfg.MnemosDBName, cfg.MnemosAutoEmbedModel, cfg.MnemosAutoEmbedDims,
		)
		registry.RegisterAll(
			builtin.NewMemoryStoreTool(mnemosClient),
			builtin.NewMemoryRecallTool(mnemosClient),
		)
	}

	// Discover skills.
	if err := registry.DiscoverSkills(cfg.SkillsDir); err != nil {
		logger.Debug("skills discovery", zap.Error(err))
	}

	// Redact secrets from tool outputs before they reach the tape/LLM.
	registry.Use(redact.Middleware(redact.New()))

	return registry
}

// DiscoverAndBuildBackgroundAgents finds agent configs in ~/.golem/agents/,
// loads each one, and builds an AgentInstance for those with remote channels.
// claimedLarkApps tracks Lark app IDs already in use to avoid duplicate
// WebSocket connections — agents whose LarkAppID is already claimed are skipped.
func DiscoverAndBuildBackgroundAgents(
	printer channel.SystemPrinter,
	logger *zap.Logger,
	claimedLarkApps map[string]bool,
) []*AgentInstance {
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

		// Skip agents whose Lark app ID is already claimed by another agent.
		if agentCfg.LarkAppID != "" && claimedLarkApps[agentCfg.LarkAppID] {
			logger.Info("skipping agent: Lark app already claimed",
				zap.String("agent", name), zap.String("app_id", agentCfg.LarkAppID))
			continue
		}

		inst, err := BuildAgent(name, agentCfg, logger.Named(name))
		if err != nil {
			logger.Error("building agent", zap.String("agent", name), zap.Error(err))
			continue
		}

		// Claim the Lark app ID after successful build.
		if inst.Config.LarkAppID != "" {
			claimedLarkApps[inst.Config.LarkAppID] = true
		}

		if printer != nil {
			printer.PrintSystem(fmt.Sprintf("Background agent %q started", name))
		}
		agents = append(agents, inst)
	}
	return agents
}
