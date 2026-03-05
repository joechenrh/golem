package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

var validLogLevels = map[string]bool{
	"debug": true, "info": true, "warn": true, "error": true,
}

// GolemHome returns the root golem configuration directory (~/.golem).
func GolemHome() string {
	return expandHome("~/.golem")
}

// Config holds all configuration for a golem agent.
type Config struct {
	// Identity
	AgentName    string // agent name (empty = default)
	SystemPrompt string // custom system prompt loaded from agent dir

	// LLM
	Model    string            // e.g. "openai:gpt-4o", "anthropic:claude-sonnet-4-20250514"
	APIKeys  map[string]string // provider name -> API key
	BaseURLs map[string]string // provider name -> custom base URL (optional)

	// Agent behavior
	MaxToolIter  int           // max tool-calling iterations per turn (default: 15)
	ShellTimeout time.Duration // shell command timeout (default: 30s)

	// Context management
	ContextStrategy string // "anchor", "masking", "hybrid" (default: "masking")

	// Executor
	Executor string // "local", "noop" (default: "local")

	// Storage
	TapeDir   string // directory for tape JSONL files (default: ~/.golem/tapes)
	SkillsDir string // skills discovery directory (default: .agent/skills)

	// Channels
	TelegramToken   string
	TelegramACL     []int64
	LarkAppID       string
	LarkAppSecret   string
	LarkVerifyToken string

	// Memory (mnemos direct mode — TiDB Cloud Serverless)
	MnemosDBHost         string // TiDB gateway host (e.g. gateway01.us-east-1.prod.aws.tidbcloud.com)
	MnemosDBUser         string // TiDB username
	MnemosDBPass         string // TiDB password
	MnemosDBName         string // database name (default: "mnemos")
	MnemosAutoEmbedModel string // auto-embed model (e.g. "tidbcloud_free/amazon/titan-embed-text-v2")
	MnemosAutoEmbedDims  int    // auto-embed dimensions (default: 1024)

	// Web
	WebSearchBackend string // "bing", "stub" (default: "bing")

	// Logging
	LogLevel string // "debug", "info", "warn", "error"
}

// Load reads config from all sources with the following precedence:
//
//  1. flagOverrides (CLI flags, only non-empty values)
//  2. Environment variables set in the shell
//  3. Per-agent config: ~/.golem/agents/<agentName>/config.env
//  4. Global config:    ~/.golem/.env
//  5. Local workspace:  .env
//  6. Hardcoded defaults
//
// agentName selects the per-agent directory; pass "" for defaults only.
func Load(agentName string, flagOverrides map[string]string) (*Config, error) {
	loadDotenvFiles(agentName)

	cfg := &Config{
		AgentName:        agentName,
		Model:            env("GOLEM_MODEL", "openai:gpt-4o"),
		MaxToolIter:      envInt("GOLEM_MAX_TOOL_ITER", 15),
		ShellTimeout:     envDuration("GOLEM_SHELL_TIMEOUT", 30*time.Second),
		ContextStrategy:  env("GOLEM_CONTEXT_STRATEGY", "masking"),
		Executor:         env("GOLEM_EXECUTOR", "local"),
		TapeDir:          expandHome(env("GOLEM_TAPE_DIR", "~/.golem/tapes")),
		SkillsDir:        env("GOLEM_SKILLS_DIR", ".agent/skills"),
		WebSearchBackend: env("GOLEM_WEB_SEARCH_BACKEND", "bing"),
		LogLevel:         env("GOLEM_LOG_LEVEL", "info"),

		// Channels
		TelegramToken:   os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramACL:     envInt64List("TELEGRAM_ALLOW_FROM"),
		LarkAppID:       os.Getenv("LARK_APP_ID"),
		LarkAppSecret:   os.Getenv("LARK_APP_SECRET"),
		LarkVerifyToken: os.Getenv("LARK_VERIFY_TOKEN"),

		// Memory (mnemos direct mode)
		MnemosDBHost:         os.Getenv("MNEMO_DB_HOST"),
		MnemosDBUser:         os.Getenv("MNEMO_DB_USER"),
		MnemosDBPass:         os.Getenv("MNEMO_DB_PASS"),
		MnemosDBName:         env("MNEMO_DB_NAME", "mnemos"),
		MnemosAutoEmbedModel: os.Getenv("MNEMO_AUTO_EMBED_MODEL"),
		MnemosAutoEmbedDims:  envInt("MNEMO_AUTO_EMBED_DIMS", 1024),
	}

	// Collect API keys and base URLs from environment.
	// Supports any provider via <PROVIDER>_API_KEY and <PROVIDER>_BASE_URL.
	cfg.APIKeys = make(map[string]string)
	cfg.BaseURLs = make(map[string]string)
	for _, e := range os.Environ() {
		key, val, ok := strings.Cut(e, "=")
		if !ok || val == "" {
			continue
		}
		if suffix, found := strings.CutSuffix(key, "_API_KEY"); found && suffix != "" {
			cfg.APIKeys[strings.ToLower(suffix)] = val
		}
		if suffix, found := strings.CutSuffix(key, "_BASE_URL"); found && suffix != "" {
			cfg.BaseURLs[strings.ToLower(suffix)] = val
		}
	}

	applyFlagOverrides(cfg, flagOverrides)

	// Load per-agent system prompt if present.
	if agentName != "" {
		promptPath := filepath.Join(GolemHome(), "agents", agentName, "system-prompt.md")
		if data, err := os.ReadFile(promptPath); err == nil {
			cfg.SystemPrompt = strings.TrimSpace(string(data))
		}
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if !validLogLevels[c.LogLevel] {
		return fmt.Errorf("invalid log level %q: must be one of debug, info, warn, error", c.LogLevel)
	}
	if c.MaxToolIter <= 0 {
		return fmt.Errorf("max tool iterations must be positive, got %d", c.MaxToolIter)
	}
	if c.ShellTimeout <= 0 {
		return fmt.Errorf("shell timeout must be positive, got %v", c.ShellTimeout)
	}
	if c.Model == "" {
		return fmt.Errorf("model must not be empty")
	}
	if strings.Count(c.Model, ":") > 1 {
		return fmt.Errorf("invalid model format %q: expected \"provider:model\" or \"model\"", c.Model)
	}
	return nil
}

// loadDotenvFiles loads .env files in increasing priority order.
// godotenv.Load only sets vars that are NOT already in the environment,
// so earlier loads take precedence. Loading order:
//  1. Per-agent config.env (highest dotenv priority)
//  2. Global ~/.golem/.env
//  3. Local .env (lowest dotenv priority)
func loadDotenvFiles(agentName string) {
	// Per-agent config (highest dotenv priority).
	if agentName != "" {
		agentEnv := filepath.Join(GolemHome(), "agents", agentName, "config.env")
		_ = godotenv.Load(agentEnv)
	}
	// Global config.
	_ = godotenv.Load(filepath.Join(GolemHome(), ".env"))
	// Local workspace .env (lowest dotenv priority).
	_ = godotenv.Load(".env")
}

// applyFlagOverrides applies CLI flag overrides (highest precedence).
func applyFlagOverrides(cfg *Config, flags map[string]string) {
	if flags == nil {
		return
	}
	overrides := map[string]*string{
		"model":            &cfg.Model,
		"tape-dir":         &cfg.TapeDir,
		"skills-dir":       &cfg.SkillsDir,
		"log-level":        &cfg.LogLevel,
		"context-strategy": &cfg.ContextStrategy,
		"executor":         &cfg.Executor,
	}
	for key, ptr := range overrides {
		if v, ok := flags[key]; ok && v != "" {
			*ptr = v
		}
	}
	// tape-dir needs home expansion.
	if v, ok := flags["tape-dir"]; ok && v != "" {
		cfg.TapeDir = expandHome(v)
	}
}

// env returns the environment variable value, or defaultVal if empty/unset.
func env(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// envInt returns the environment variable as int, or defaultVal.
func envInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return defaultVal
}

// envDuration returns the environment variable as time.Duration, or defaultVal.
func envDuration(key string, defaultVal time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return defaultVal
}

// envInt64List parses a comma-separated list of int64 values from an env var.
func envInt64List(key string) []int64 {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	var result []int64
	for _, s := range strings.Split(v, ",") {
		s = strings.TrimSpace(s)
		if id, err := strconv.ParseInt(s, 10, 64); err == nil {
			result = append(result, id)
		}
	}
	return result
}

// HasRemoteChannels returns true if any remote channel credentials are configured
// (Lark or Telegram), meaning this agent can receive messages without the CLI.
func (c *Config) HasRemoteChannels() bool {
	if c.LarkAppID != "" && c.LarkAppSecret != "" {
		return true
	}
	if c.TelegramToken != "" {
		return true
	}
	return false
}

// DiscoverAgents reads ~/.golem/agents/ and returns the names of all
// subdirectories. Each subdirectory represents a named agent configuration.
// Returns nil (not an error) if the directory does not exist.
func DiscoverAgents() ([]string, error) {
	agentsDir := filepath.Join(GolemHome(), "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading agents dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") || path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[1:])
	}
	return path
}
