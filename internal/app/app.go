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
	"github.com/joechenrh/golem/internal/exthook"
	"github.com/joechenrh/golem/internal/fs"
	"github.com/joechenrh/golem/internal/hooks"
	"github.com/joechenrh/golem/internal/llm"

	"github.com/joechenrh/golem/internal/middleware"
	"github.com/joechenrh/golem/internal/redact"
	"github.com/joechenrh/golem/internal/scheduler"
	"github.com/joechenrh/golem/internal/tape"
	"github.com/joechenrh/golem/internal/tools"
	"github.com/joechenrh/golem/internal/tools/builtin"
)

// ErrAgentQuit is returned by Run when the user requests quit via the CLI.
var ErrAgentQuit = errors.New("agent quit")

const (
	// incomingMsgBufSize is the capacity of the incoming message channel.
	incomingMsgBufSize = 100
	// streamTokenBufSize is the capacity of the streaming token channel.
	streamTokenBufSize = 100
	// cacheTTL is how long read-only tool results are cached.
	cacheTTL = 60 * time.Second
)

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
	inCh := make(chan channel.IncomingMessage, incomingMsgBufSize)
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
			inst.Logger.Debug("per-chat worker spawned",
				zap.String("chat_id", msg.ChannelID))
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
	start := time.Now()
	inst.Logger.Debug("incoming message",
		zap.String("channel", msg.ChannelName),
		zap.String("chat_id", msg.ChannelID),
		zap.Int("text_len", len(msg.Text)))

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

	// Handle slash commands for remote channels (e.g. /help, /new, /status).
	if msg.ChannelName != "cli" && strings.HasPrefix(msg.Text, "/") {
		if inst.handleSlashCommand(sessCtx, ch, msg) {
			return false
		}
	}

	if ch.SupportsStreaming() {
		tokenCh := make(chan string, streamTokenBufSize)

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
			inst.sendErrorFeedback(sessCtx, ch, msg.ChannelID, err)
		}
	} else {
		response, err := sess.HandleInput(sessCtx, msg)
		if err != nil {
			if errors.Is(err, agent.ErrQuit) {
				return true
			}
			inst.sendErrorFeedback(sessCtx, ch, msg.ChannelID, err)
			return false
		}
		if err := ch.Send(sessCtx, channel.OutgoingMessage{ChannelID: msg.ChannelID, Text: response}); err != nil {
			inst.Logger.Error("send error", zap.Error(err))
		}
	}

	inst.Logger.Info("dispatch complete",
		zap.String("channel", msg.ChannelName),
		zap.String("chat_id", msg.ChannelID),
		zap.Int64("elapsed_ms", time.Since(start).Milliseconds()))

	return false
}

// handleSlashCommand processes slash commands from remote channels.
// Returns true if the command was handled.
func (inst *AgentInstance) handleSlashCommand(
	ctx context.Context, ch channel.Channel, msg channel.IncomingMessage,
) bool {
	cmd := strings.TrimSpace(strings.TrimPrefix(msg.Text, "/"))
	parts := strings.SplitN(cmd, " ", 2)

	var response string
	switch parts[0] {
	case "help":
		response = "**Available commands:**\n" +
			"- `/help` — Show this help message\n" +
			"- `/new` — Start a fresh conversation\n" +
			"- `/status` — Show session info\n\n" +
			"Send any other message to chat with the bot."
	case "new":
		if inst.Sessions != nil {
			inst.Sessions.Reset(msg.ChannelID)
		}
		response = "Session reset. Starting a fresh conversation."
	case "status":
		if inst.Sessions == nil {
			response = "No session manager configured."
		} else if sess := inst.Sessions.Get(msg.ChannelID); sess == nil {
			response = "No active session for this chat."
		} else {
			response = sess.StatusInfo()
		}
	default:
		return false
	}

	ch.SendDirect(ctx, msg.ChannelID, response)
	return true
}

// sendErrorFeedback sends a user-facing error message via the channel's
// SendError method and logs the real error server-side.
func (inst *AgentInstance) sendErrorFeedback(
	ctx context.Context, ch channel.Channel, channelID string, err error,
) {
	inst.Logger.Error("message processing error", zap.Error(err))
	ch.SendError(ctx, channelID, "Something went wrong. Please try again later.")
}

// BuildAgent creates a fully wired AgentInstance from config.
// When name is "default", a CLI channel is created for interactive use.
// Otherwise, no CLI channel is added (background agent).
// Lark/Telegram channels are always conditional on config credentials.
func BuildAgent(
	name string, cfg *config.Config, logger *zap.Logger,
) (*AgentInstance, error) {
	llmClient, err := BuildLLMClient(cfg, logger)
	if err != nil {
		return nil, err
	}

	classifierLLM, err := BuildClassifierClient(cfg, logger)
	if err != nil {
		return nil, fmt.Errorf("classifier client: %w", err)
	}

	agentTapeDir, err := tape.AgentDir(cfg.TapeDir, name)
	if err != nil {
		return nil, err
	}
	tapePath := filepath.Join(agentTapeDir, fmt.Sprintf("session-%s.jsonl", time.Now().Format("20060102-150405")))
	tapeStore, err := tape.NewFileStore(tapePath)
	if err != nil {
		return nil, fmt.Errorf("tape store: %w", err)
	}

	exec, filesystem, err := buildExecutorAndFS(cfg)
	if err != nil {
		return nil, err
	}

	ctxStrategy, err := ctxmgr.NewContextStrategy(cfg.ContextStrategy)
	if err != nil {
		return nil, fmt.Errorf("context strategy: %w", err)
	}

	// Wire LLM client into HybridStrategy for summarization.
	_, modelName := llm.ParseModelProvider(cfg.Model)
	if hs, ok := ctxStrategy.(*ctxmgr.HybridStrategy); ok {
		hs.LLM = llmClient
		hs.Model = modelName
	}

	hookBus, metricsHook := buildHookBus(logger, agentTapeDir)
	channels, printer, larkCh := buildChannels(name, cfg, logger)
	schedStore := buildScheduleStore(name, logger)

	// Tool factory builds a fresh registry with all tools including
	// schedule tools. Shared by the default session, SessionManager,
	// and scheduler.
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

	// spawnToolFactory wraps toolFactory and adds spawn_agent.
	// Sub-agents use the base toolFactory (no spawn capability) to prevent
	// recursive spawning.
	var subAgentSeq atomic.Int64
	spawnToolFactory := func() *tools.Registry {
		r := toolFactory()
		r.Register(builtin.NewSpawnAgentTool(func(ctx context.Context, prompt string) (string, error) {
			seq := subAgentSeq.Add(1)
			subTapePath := filepath.Join(agentTapeDir, fmt.Sprintf("sub-%d-%s.jsonl", seq, time.Now().Format("20060102-150405")))
			sess, err := buildEphemeralSession(llmClient, cfg, toolFactory, logger, "sub-agent", subTapePath)
			if err != nil {
				return "", fmt.Errorf("sub-agent: %w", err)
			}

			return sess.HandleInput(ctx, channel.IncomingMessage{
				ChannelName: "internal",
				Text:        prompt,
			})
		}))
		return r
	}

	registry := spawnToolFactory()

	// Register create_skill tool for named agents.
	if name != "" {
		registry.Register(builtin.NewCreateSkillTool(name, registry.GetSkillStore()))
	}

	// Build skill directories for periodic reload.
	skillDirs := buildSkillDirs(cfg)

	extHookRunner := buildExtHookRunner(name, logger)

	defaultSess := agent.NewSession(llmClient, classifierLLM, registry, tapeStore, ctxStrategy, hookBus, cfg, logger, name)
	defaultSess.MetricsSummary = metricsHook.Summary
	defaultSess.SetSkillReload(skillDirs, cfg.SkillReloadInterval)
	defaultSess.SetExtHooks(extHookRunner)

	// Wire OnDrop callback for HybridStrategy to save dropped context via hooks.
	if hs, ok := ctxStrategy.(*ctxmgr.HybridStrategy); ok && extHookRunner != nil {
		agentNameForHook := name
		hs.OnDrop = func(ctx context.Context, dropped []llm.Message) {
			var sb strings.Builder
			for _, m := range dropped {
				fmt.Fprintf(&sb, "[%s]: %s\n", m.Role, m.Content)
			}
			extHookRunner.Run(ctx, "context_dropped", agentNameForHook, map[string]any{
				"dropped_text":  sb.String(),
				"dropped_count": len(dropped),
			})
		}
	}

	// Wire card action handler and session manager for remote channels.
	var sessions *agent.SessionManager
	if cfg.HasRemoteChannels() {
		sessions = buildSessionManager(name, cfg, llmClient, classifierLLM,
			spawnToolFactory, metricsHook, agentTapeDir, skillDirs, extHookRunner, logger)
	}
	if larkCh != nil {
		setupCardActionHandler(larkCh, sessions, logger)
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
		toolFactory: toolFactory,
	}, nil
}

// buildSessionManager creates and populates a SessionManager for remote channels.
func buildSessionManager(
	name string, cfg *config.Config,
	llmClient, classifierLLM llm.Client,
	toolFactory func() *tools.Registry,
	metricsHook *hooks.MetricsHook,
	auditDir string,
	skillDirs []string,
	extHookRunner agent.ExtHookRunner,
	logger *zap.Logger,
) *agent.SessionManager {
	sessions := agent.NewSessionManager(agent.SessionFactory{
		LLMClient:       llmClient,
		ClassifierLLM:   classifierLLM,
		Config:          cfg,
		Logger:          logger,
		ToolFactory:     toolFactory,
		ContextStrategy: cfg.ContextStrategy,
		AgentName:       name,
		MetricsHook:     metricsHook,
		AuditDir:        auditDir,
		SkillDirs:       skillDirs,
		ExtHookRunner:   extHookRunner,
	}, logger)
	if err := sessions.LoadExisting(cfg.TapeDir); err != nil {
		logger.Warn("failed to restore sessions", zap.Error(err))
	}
	return sessions
}

// setupCardActionHandler wires the Lark card action handler for session reset
// and user feedback buttons.
func setupCardActionHandler(
	larkCh *larkchan.LarkChannel,
	sessions *agent.SessionManager,
	logger *zap.Logger,
) {
	larkCh.SetCardActionHandler(func(chatID string, action map[string]any) {
		act, _ := action["action"].(string)
		switch act {
		case "reset_session":
			if sessions != nil {
				sessions.Reset(chatID)
			}
			larkCh.SendDirect(context.Background(), chatID, "Session reset. Starting a fresh conversation.")
		case "feedback":
			val, _ := action["value"].(string)
			if sessions != nil {
				if sess := sessions.Get(chatID); sess != nil {
					sess.RecordFeedback(chatID, val)
				}
			}
			logger.Info("user feedback received",
				zap.String("chat_id", chatID), zap.String("value", val))
		}
	})
}

// buildSkillDirs returns the global and per-agent skill directories.
func buildSkillDirs(cfg *config.Config) []string {
	var dirs []string
	globalDir := filepath.Join(config.GolemHome(), "skills")
	dirs = append(dirs, globalDir)
	if cfg.AgentName != "" {
		agentDir := filepath.Join(config.GolemHome(), "agents", cfg.AgentName, "skills")
		dirs = append(dirs, agentDir)
	}
	return dirs
}

// buildExtHookRunner discovers external hooks from global and agent-specific
// directories and returns a runner, or nil if no hooks are found.
func buildExtHookRunner(name string, logger *zap.Logger) agent.ExtHookRunner {
	if name == "" {
		return nil
	}

	var allHooks []*exthook.HookDef
	for _, dir := range []string{
		filepath.Join(config.GolemHome(), "hooks"),
		filepath.Join(config.GolemHome(), "agents", name, "hooks"),
	} {
		hks, err := exthook.Discover(dir)
		if err != nil {
			if !os.IsNotExist(err) {
				logger.Warn("failed to discover hooks", zap.String("dir", dir), zap.Error(err))
			}
			continue
		}
		allHooks = append(allHooks, hks...)
	}

	for _, h := range allHooks {
		logger.Info("loaded external hook",
			zap.String("name", h.Name),
			zap.String("command", h.Command),
			zap.Any("events", h.Events))
	}
	if len(allHooks) > 0 {
		return exthook.NewRunner(allHooks, logger)
	}
	return nil
}

// buildExecutorAndFS creates the command executor and sandboxed filesystem.
func buildExecutorAndFS(cfg *config.Config) (executor.Executor, *fs.LocalFS, error) {
	if err := os.MkdirAll(cfg.WorkspaceDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("creating workspace dir: %w", err)
	}

	var exec executor.Executor
	switch cfg.Executor {
	case "noop":
		exec = executor.NewNoop()
	default:
		exec = executor.NewLocal(cfg.WorkspaceDir)
	}

	filesystem, err := fs.NewLocalFS(cfg.WorkspaceDir)
	if err != nil {
		return nil, nil, fmt.Errorf("filesystem: %w", err)
	}
	return exec, filesystem, nil
}

// buildHookBus creates the event bus with standard hooks (logging, safety,
// metrics, audit).
func buildHookBus(logger *zap.Logger, agentTapeDir string) (*hooks.Bus, *hooks.MetricsHook) {
	auditPath := filepath.Join(agentTapeDir, fmt.Sprintf("audit-%s.jsonl", time.Now().Format("20060102-150405")))
	return hooks.BuildDefaultBus(logger, nil, auditPath)
}

// buildChannels creates the channel map based on the agent name and config.
func buildChannels(
	name string, cfg *config.Config, logger *zap.Logger,
) (map[string]channel.Channel, channel.SystemPrinter, *larkchan.LarkChannel) {
	channels := make(map[string]channel.Channel)

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

		// Start HTTP server for card action callbacks if port is configured.
		if cfg.LarkCallbackPort != "" {
			startCardCallbackServer(cfg.LarkCallbackPort, larkCh, logger)
		}
	}

	return channels, printer, larkCh
}

// startCardCallbackServer starts a background HTTP server for Lark card
// action callbacks (button clicks for reset session, feedback).
func startCardCallbackServer(port string, larkCh *larkchan.LarkChannel, logger *zap.Logger) {
	mux := http.NewServeMux()
	mux.HandleFunc("/lark/card/action", larkCh.CardActionHTTPHandler())

	go func() {
		addr := ":" + port
		logger.Info("starting Lark card callback server", zap.String("addr", addr))
		if err := http.ListenAndServe(addr, mux); err != nil {
			logger.Error("card callback server error", zap.Error(err))
		}
	}()
}

// buildScheduleStore creates the per-agent schedule persistence store.
func buildScheduleStore(name string, logger *zap.Logger) *scheduler.Store {
	if name == "" {
		return nil
	}
	schedPath := filepath.Join(config.GolemHome(), "agents", name, "schedules.json")
	store := scheduler.NewStore(schedPath)
	if err := store.Load(); err != nil {
		logger.Warn("failed to load schedules", zap.Error(err))
	}
	return store
}

// buildProviderClient creates an LLM client for the given model string,
// auto-registering unknown providers as OpenAI-compatible.
func buildProviderClient(model string, cfg *config.Config, logger *zap.Logger, opts ...llm.ClientOption) (llm.Client, error) {
	provider, _ := llm.ParseModelProvider(model)
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

	if baseURL := cfg.BaseURLs[string(provider)]; baseURL != "" {
		opts = append(opts, llm.WithBaseURL(baseURL))
	}
	if logger != nil {
		opts = append(opts, llm.WithLogger(logger))
	}
	client, err := llm.NewClient(provider, apiKey, opts...)
	if err != nil {
		return nil, err
	}
	return llm.NewRateLimitedClient(client, cfg.LLMRateLimit, logger), nil
}

// BuildLLMClient creates an LLM client from the config, auto-registering
// unknown providers as OpenAI-compatible.
func BuildLLMClient(cfg *config.Config, logger *zap.Logger) (llm.Client, error) {
	var opts []llm.ClientOption
	if cfg.UseResponsesAPI {
		opts = append(opts, llm.WithResponsesAPI())
	}
	return buildProviderClient(cfg.Model, cfg, logger, opts...)
}

// BuildClassifierClient creates a lightweight LLM client for nudge
// classification. Returns nil if ClassifierModel is not configured.
func BuildClassifierClient(cfg *config.Config, logger *zap.Logger) (llm.Client, error) {
	if cfg.ClassifierModel == "" {
		return nil, nil
	}
	client, err := buildProviderClient(cfg.ClassifierModel, cfg, logger.Named("classifier"))
	if err != nil {
		return nil, fmt.Errorf("classifier client: %w", err)
	}
	return client, nil
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
	// When native web search is enabled (Responses API handles search server-side),
	// still register the builtin web_search so it appears in tool definitions —
	// the Responses API request builder replaces it with web_search_preview.
	// Keep web_fetch and http_request regardless.
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
			builtin.NewChatHistoryTool(larkCh),
		)
		registry.Expand("lark_send")
		registry.Expand("lark_list_chats")
		registry.Expand("lark_read_doc")
		registry.Expand("lark_write_doc")
		registry.Expand("chat_history")
	}

	// Persona self-edit tool (only when persona is configured).
	if cfg.Persona.HasPersona() {
		registry.Register(builtin.NewPersonaSelfTool(cfg.Persona))
	}

	// Discover skills into a SkillStore, then register the single "skill" tool.
	skillStore := tools.NewSkillStore()
	globalSkillsDir := filepath.Join(config.GolemHome(), "skills")
	if err := skillStore.Discover(globalSkillsDir); err != nil {
		logger.Debug("global skills discovery", zap.Error(err))
	}
	if cfg.AgentName != "" {
		agentSkillsDir := filepath.Join(config.GolemHome(), "agents", cfg.AgentName, "skills")
		if err := skillStore.Discover(agentSkillsDir); err != nil {
			logger.Debug("agent skills discovery", zap.Error(err))
		}
	}
	registry.SetSkillStore(skillStore)
	registry.Register(builtin.NewSkillTool(skillStore, registry))
	registry.Expand("skill") // always expanded so LLM sees the skill list

	// Export GOLEM_HOME so external tool manifests can reference $GOLEM_HOME
	// in their command/args and have it resolved at load time.
	golemHome := config.GolemHome()
	if os.Getenv("GOLEM_HOME") == "" {
		os.Setenv("GOLEM_HOME", golemHome)
	}

	// Load external plugin tools from ~/.golem/plugins/.
	pluginDir := filepath.Join(golemHome, "plugins")
	extTools, err := tools.LoadExternalTools(pluginDir, logger)
	if err != nil {
		logger.Warn("external tool loading failed", zap.Error(err))
	}
	for _, et := range extTools {
		registry.Register(et)
		logger.Info("loaded external tool", zap.String("name", et.Name()))
	}

	// Load MCP server tools from ~/.golem/plugins/*.mcp.json.
	mcpTools, _, err := tools.LoadMCPServers(pluginDir, logger)
	if err != nil {
		logger.Warn("MCP server loading failed", zap.Error(err))
	}
	for _, mt := range mcpTools {
		registry.Register(mt)
	}

	// Per-tool access control: allow/deny lists from config.
	if len(cfg.ToolAllow) > 0 || len(cfg.ToolDeny) > 0 {
		registry.Use(middleware.ACL(cfg.ToolAllow, cfg.ToolDeny))
	}

	// Cache read-only tool results to avoid redundant calls.
	// Mutating tools invalidate the cache so reads never return stale data.
	cache := middleware.NewCacheMiddleware(cacheTTL, []string{
		"read_file", "list_directory", "search_files", "web_search", "web_fetch",
	}, []string{
		"write_file", "edit_file", "shell_exec",
		"lark_send", "lark_write_doc",
		"create_skill", "schedule_add", "schedule_remove",
		"persona_self",
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
	sess, err := buildEphemeralSession(f.llmClient, f.cfg, f.toolFactory, f.logger, "scheduler", fullTapePath)
	if err != nil {
		return "", fmt.Errorf("scheduler: %w", err)
	}

	return sess.HandleInput(ctx, msg)
}

// buildEphemeralSession creates a short-lived session with its own tape,
// subAgentMaxToolIter is the iteration limit for sub-agent sessions,
// higher than the default to allow complex multi-step tasks.
const subAgentMaxToolIter = 50

// context strategy, hook bus, and tool registry. Used by spawn_agent and
// the scheduler to avoid duplicating session setup logic.
func buildEphemeralSession(
	llmClient llm.Client,
	cfg *config.Config,
	toolFactory func() *tools.Registry,
	logger *zap.Logger,
	name string,
	tapePath string,
) (*agent.Session, error) {
	tapeStore, err := tape.NewFileStore(tapePath)
	if err != nil {
		return nil, fmt.Errorf("tape: %w", err)
	}

	// Sub-agents get a higher iteration limit so they can handle
	// complex multi-step tasks without hitting the cap.
	subCfg := *cfg
	subCfg.MaxToolIter = subAgentMaxToolIter

	ctxStrategy, _ := ctxmgr.NewContextStrategy(cfg.ContextStrategy)
	auditPath := filepath.Join(filepath.Dir(tapePath), fmt.Sprintf("audit-ephemeral-%s.jsonl", time.Now().Format("20060102-150405")))
	hookBus, _ := hooks.BuildDefaultBus(logger.Named(name), nil, auditPath)

	registry := toolFactory()
	return agent.NewSession(llmClient, nil, registry, tapeStore, ctxStrategy, hookBus, &subCfg, logger.Named(name), name), nil
}
