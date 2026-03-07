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
	"github.com/joechenrh/golem/internal/middleware"
	"github.com/joechenrh/golem/internal/redact"
	"github.com/joechenrh/golem/internal/scheduler"
	"github.com/joechenrh/golem/internal/tape"
	"github.com/joechenrh/golem/internal/tools"
	"github.com/joechenrh/golem/internal/tools/builtin"
)

// ErrAgentQuit is returned by Run when the user requests quit via the CLI.
var ErrAgentQuit = errors.New("agent quit")

// AgentInstance bundles a running agent with its channels and metadata.
type AgentInstance struct {
	Name       string
	Config     *config.Config
	Logger     *zap.Logger
	LLMClient  llm.Client
	Session    *agent.Session        // default session (used by CLI)
	Sessions   *agent.SessionManager // per-chat sessions (used by remote channels)
	Channels   map[string]channel.Channel
	Printer    channel.SystemPrinter // formatted output (e.g. CLI channel)
	Registry   *tools.Registry
	TapePath   string
	SchedStore *scheduler.Store     // schedule persistence (nil if no agent name)
	Sched      *scheduler.Scheduler // background scheduler (nil until Run)

	MetricsHook *hooks.MetricsHook // exposed for the metrics collector

	// toolFactory builds a fresh tool registry for ephemeral sessions (scheduler, sub-agents).
	toolFactory func() *tools.Registry
}

// Run starts all channels, processes incoming messages, and blocks until the
// agent is done. It uses an explicit cancel and two errgroups:
//   - An inner plain errgroup tracks channel goroutines; when all channels
//     exit, inCh is closed so the message processor drains and returns.
//   - An outer plain errgroup ties the message processor and the channel
//     closer together.
//
// gcancel is called when any channel exits (e.g. CLI EOF) or when the user
// types :quit, ensuring all remaining channels stop promptly.
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

	// Start the scheduler if schedules are configured.
	if inst.SchedStore != nil {
		factory := &appSessionFactory{
			llmClient:   inst.LLMClient,
			cfg:         inst.Config,
			logger:      inst.Logger,
			toolFactory: inst.toolFactory,
			agentName:   inst.Name,
		}
		inst.Sched = scheduler.New(inst.SchedStore, inst.Channels, factory, inst.Logger)
		go inst.Sched.Run(gctx)
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

	// Select the appropriate Session: per-chat for remote channels, the
	// default session for CLI. For managed sessions, use the session's
	// context so that eviction cancels in-flight work.
	sess := inst.Session
	sessCtx := ctx
	if inst.Sessions != nil && msg.ChannelName != "cli" {
		var err error
		sess, err = inst.Sessions.GetOrCreate(msg.ChannelID)
		if err != nil {
			inst.Logger.Error("failed to get or create session",
				zap.String("channel_id", msg.ChannelID), zap.Error(err))
			return false
		}
		if sess.Context() != nil {
			sessCtx = sess.Context()
		}
	}

	if ch.SupportsStreaming() {
		tokenCh := make(chan string, 100)

		streamDone := make(chan struct{})
		go func() {
			defer close(streamDone)
			if err := ch.SendStream(sessCtx, msg.ChannelID, tokenCh); err != nil {
				inst.Logger.Error("stream send error", zap.Error(err))
			}
		}()

		err := sess.HandleInputStream(sessCtx, msg, tokenCh)
		close(tokenCh)
		<-streamDone
		if err != nil {
			if errors.Is(err, agent.ErrQuit) {
				return true
			}
			inst.logOrPrintError(ch, "Error: "+err.Error())
		}
	} else {
		response, err := sess.HandleInput(sessCtx, msg)
		if err != nil {
			if errors.Is(err, agent.ErrQuit) {
				return true
			}
			inst.logOrPrintError(ch, "Error: "+err.Error())
			return false
		}
		if err := ch.Send(sessCtx, channel.OutgoingMessage{ChannelID: msg.ChannelID, Text: response}); err != nil {
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
	agentTapeDir, err := tape.AgentDir(cfg.TapeDir, name)
	if err != nil {
		return nil, err
	}
	tapePath := filepath.Join(agentTapeDir, fmt.Sprintf("session-%s.jsonl", time.Now().Format("20060102-150405")))
	tapeStore, err := tape.NewFileStore(tapePath)
	if err != nil {
		return nil, fmt.Errorf("tape store: %w", err)
	}

	// 3. Initialize executor and filesystem.
	workDir := cfg.WorkspaceDir
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating workspace dir: %w", err)
	}

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
	hookBus.Register(hooks.NewSafetyHook())
	metricsHook := hooks.NewMetricsHook()
	hookBus.Register(metricsHook)

	auditPath := filepath.Join(agentTapeDir, fmt.Sprintf("audit-%s.jsonl", time.Now().Format("20060102-150405")))
	auditHook, err := hooks.NewAuditHook(auditPath)
	if err != nil {
		logger.Warn("failed to create audit hook", zap.Error(err))
	} else {
		hookBus.Register(auditHook)
	}

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

	// 7. Initialize schedule store (per-agent persistence).
	var schedStore *scheduler.Store
	if name != "" {
		schedPath := filepath.Join(config.GolemHome(), "agents", name, "schedules.json")
		schedStore = scheduler.NewStore(schedPath)
		if err := schedStore.Load(); err != nil {
			logger.Warn("failed to load schedules", zap.Error(err))
		}
	}

	// 8. Build tool registry.
	registry := BuildToolRegistry(cfg, exec, filesystem, larkCh, logger)

	// Register schedule tools on the default registry.
	// The scheduler reference (nil here) is set later in Run() once the Scheduler exists.
	if schedStore != nil {
		registry.RegisterAll(
			builtin.NewScheduleAddTool(schedStore, nil),
			builtin.NewScheduleListTool(schedStore),
			builtin.NewScheduleRemoveTool(schedStore, nil),
		)
		registry.Expand("schedule_add")
		registry.Expand("schedule_list")
		registry.Expand("schedule_remove")
	}

	// 9. Register spawn_agent tool. The runner creates a sub-agent that
	//    shares the LLM client and infra but has its own tape and registry
	//    WITHOUT spawn capability (prevents recursive spawning).
	var subAgentSeq atomic.Int64
	runner := func(ctx context.Context, prompt string) (string, error) {
		seq := subAgentSeq.Add(1)
		subTapePath := filepath.Join(agentTapeDir, fmt.Sprintf("sub-%d-%s.jsonl", seq, time.Now().Format("20060102-150405")))
		subTape, err := tape.NewFileStore(subTapePath)
		if err != nil {
			return "", fmt.Errorf("sub-agent tape: %w", err)
		}
		subCtxStrategy, _ := ctxmgr.NewContextStrategy(cfg.ContextStrategy)
		subHooks := hooks.NewBus(logger.Named("sub-agent"))
		subHooks.Register(hooks.NewLoggingHook(logger.Named("sub-agent")))

		// Build a registry without spawn_agent.
		subRegistry := BuildToolRegistry(cfg, exec, filesystem, larkCh, logger)

		subSess := agent.NewSession(llmClient, subRegistry, subTape, subCtxStrategy, subHooks, cfg, logger.Named("sub-agent"))

		msg := channel.IncomingMessage{
			ChannelName: "internal",
			Text:        prompt,
		}
		return subSess.HandleInput(ctx, msg)
	}
	registry.Register(builtin.NewSpawnAgentTool(runner))

	// 10. Create default session.
	defaultSess := agent.NewSession(llmClient, registry, tapeStore, ctxStrategy, hookBus, cfg, logger)
	defaultSess.MetricsSummary = metricsHook.Summary

	// 11. Create SessionManager for agents with remote channels.
	// Each remote chat gets its own Session with isolated tape and tools.
	var sessions *agent.SessionManager
	if cfg.HasRemoteChannels() {
		toolFactory := func() *tools.Registry {
			r := BuildToolRegistry(cfg, exec, filesystem, larkCh, logger)
			if schedStore != nil {
				r.RegisterAll(
					builtin.NewScheduleAddTool(schedStore, nil),
					builtin.NewScheduleListTool(schedStore),
					builtin.NewScheduleRemoveTool(schedStore, nil),
				)
				r.Expand("schedule_add")
				r.Expand("schedule_list")
				r.Expand("schedule_remove")
			}
			return r
		}
		sessions = agent.NewSessionManager(agent.SessionFactory{
			LLMClient:       llmClient,
			Config:          cfg,
			Logger:          logger,
			ToolFactory:     toolFactory,
			ContextStrategy: cfg.ContextStrategy,
			AgentName:       name,
			MetricsHook:     metricsHook,
		}, logger)

		// Restore sessions from existing tape files.
		if err := sessions.LoadExisting(cfg.TapeDir); err != nil {
			logger.Warn("failed to restore sessions", zap.Error(err))
		}
	}

	// Build a reusable tool factory for ephemeral sessions (scheduler).
	schedToolFactory := func() *tools.Registry {
		r := BuildToolRegistry(cfg, exec, filesystem, larkCh, logger)
		if schedStore != nil {
			r.RegisterAll(
				builtin.NewScheduleAddTool(schedStore, nil),
				builtin.NewScheduleListTool(schedStore),
				builtin.NewScheduleRemoveTool(schedStore, nil),
			)
			r.Expand("schedule_add")
			r.Expand("schedule_list")
			r.Expand("schedule_remove")
		}
		return r
	}

	return &AgentInstance{
		Name:        name,
		Config:      cfg,
		Logger:      logger,
		LLMClient:   llmClient,
		Session:     defaultSess,
		Sessions:    sessions,
		Channels:    channels,
		Printer:     printer,
		Registry:    registry,
		TapePath:    tapePath,
		SchedStore:  schedStore,
		MetricsHook: metricsHook,
		toolFactory: schedToolFactory,
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
		builtin.NewHTTPRequestTool(webClient),
	)

	// Lark tools (pre-expanded for immediate full schema).
	if larkCh != nil {
		registry.RegisterAll(
			builtin.NewLarkSendTool(larkCh, webClient),
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

	// Persona self-edit tool (only when persona is configured).
	if cfg.Persona.HasPersona() {
		registry.Register(builtin.NewPersonaSelfTool(cfg.Persona))
	}

	// Discover skills.
	if err := registry.DiscoverSkills(cfg.SkillsDir); err != nil {
		logger.Debug("skills discovery", zap.Error(err))
	}

	// Load external plugin tools from ~/.golem/plugins/.
	pluginDir := filepath.Join(config.GolemHome(), "plugins")
	extTools, err := tools.LoadExternalTools(pluginDir)
	if err != nil {
		logger.Warn("external tool loading failed", zap.Error(err))
	}
	for _, et := range extTools {
		registry.Register(et)
		logger.Info("loaded external tool", zap.String("name", et.Name()))
	}

	// Cache read-only tool results to avoid redundant calls.
	// Mutating tools invalidate the cache so reads never return stale data.
	cache := middleware.NewCacheMiddleware(60*time.Second, []string{
		"read_file", "list_directory", "search_files", "web_search", "web_fetch",
	}, []string{
		"write_file", "edit_file", "shell_exec",
	})
	registry.Use(cache.Middleware())

	// Redact secrets from tool outputs before they reach the tape/LLM.
	registry.Use(middleware.Redact(redact.New()))

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

// appSessionFactory implements scheduler.SessionFactory by creating ephemeral
// sessions for scheduled task execution.
type appSessionFactory struct {
	llmClient   llm.Client
	cfg         *config.Config
	logger      *zap.Logger
	toolFactory func() *tools.Registry
	agentName   string
}

func (f *appSessionFactory) HandleScheduledPrompt(
	ctx context.Context, tapePath string, msg channel.IncomingMessage,
) (string, error) {
	agentDir, err := tape.AgentDir(f.cfg.TapeDir, f.agentName)
	if err != nil {
		return "", err
	}
	fullTapePath := filepath.Join(agentDir, tapePath)
	tapeStore, err := tape.NewFileStore(fullTapePath)
	if err != nil {
		return "", fmt.Errorf("scheduler tape: %w", err)
	}

	ctxStrategy, _ := ctxmgr.NewContextStrategy(f.cfg.ContextStrategy)
	hookBus := hooks.NewBus(f.logger.Named("scheduler"))
	hookBus.Register(hooks.NewLoggingHook(f.logger.Named("scheduler")))

	registry := f.toolFactory()
	sess := agent.NewSession(f.llmClient, registry, tapeStore, ctxStrategy, hookBus, f.cfg, f.logger.Named("scheduler"))

	return sess.HandleInput(ctx, msg)
}
