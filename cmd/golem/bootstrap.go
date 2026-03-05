package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

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
	"github.com/joechenrh/golem/internal/tape"
	"github.com/joechenrh/golem/internal/tools"
	"github.com/joechenrh/golem/internal/tools/builtin"
)

// AgentInstance bundles a running agent with its channels and metadata.
type AgentInstance struct {
	Loop     *agent.AgentLoop
	Channels map[string]channel.Channel
	Registry *tools.Registry
	TapePath string
}

// buildAgent creates a fully wired AgentInstance from config.
// The returned instance owns its own LLM client, tape store, tool registry,
// channels, and agent loop — making it safe to call multiple times for
// multi-agent setups.
func buildAgent(cfg *config.Config, logger *zap.Logger) (*AgentInstance, error) {
	// 1. Initialize LLM client.
	llmClient, err := buildLLMClient(cfg)
	if err != nil {
		return nil, err
	}

	// 2. Initialize tape store.
	if err := os.MkdirAll(cfg.TapeDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating tape dir: %w", err)
	}
	tapePath := filepath.Join(cfg.TapeDir, fmt.Sprintf("session-%s.jsonl", time.Now().Format("20060102-150405")))
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
	cliCh := cli.New()
	channels := map[string]channel.Channel{"cli": cliCh}

	var larkCh *larkchan.LarkChannel
	if cfg.LarkAppID != "" && cfg.LarkAppSecret != "" {
		larkCh = larkchan.New(cfg.LarkAppID, cfg.LarkAppSecret, cfg.LarkVerifyToken, logger)
		channels["lark"] = larkCh
	}

	// 7. Build tool registry.
	registry := buildToolRegistry(cfg, exec, filesystem, larkCh, logger)

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
		subRegistry := buildToolRegistry(cfg, exec, filesystem, larkCh, logger)

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

	return &AgentInstance{
		Loop:     loop,
		Channels: channels,
		Registry: registry,
		TapePath: tapePath,
	}, nil
}

// buildLLMClient creates an LLM client from the config, auto-registering
// unknown providers as OpenAI-compatible.
func buildLLMClient(cfg *config.Config) (llm.Client, error) {
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
	return llm.NewClient(provider, apiKey, opts...)
}

// buildToolRegistry creates and populates a tool registry with all built-in
// tools, channel-specific tools, and discovered skills.
// The registry intentionally does NOT include spawn_agent — that is added
// separately by buildAgent to prevent sub-agents from spawning recursively.
func buildToolRegistry(
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
		)
		registry.Expand("lark_send")
		registry.Expand("lark_list_chats")
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

	return registry
}
