package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"
)

var validLogLevels = map[string]bool{
	"debug": true, "info": true, "warn": true, "error": true,
}

// Persona holds the three-layer identity files for an agent.
//
//	Layer 1 (Identity): Soul, User
//	Layer 2 (Operations): Agents
//	Layer 3 (Knowledge): Memory
type Persona struct {
	mu sync.RWMutex

	Soul   string // SOUL.md — core identity and personality
	User   string // USER.md — who the agent serves (global, shared)
	Agents string // AGENTS.md — behavioral rules
	Memory string // MEMORY.md — curated persistent knowledge

	SoulPath   string // absolute path to SOUL.md
	AgentsPath string // absolute path to AGENTS.md
	MemoryPath string // absolute path to MEMORY.md
}

// HasPersona reports whether persona files are configured (SOUL.md exists).
func (p *Persona) HasPersona() bool {
	return p != nil && p.GetSoul() != ""
}

// GetSoul returns the Soul content (thread-safe).
func (p *Persona) GetSoul() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.Soul
}

// SetSoul updates the Soul content (thread-safe).
func (p *Persona) SetSoul(s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Soul = s
}

// GetAgents returns the Agents content (thread-safe).
func (p *Persona) GetAgents() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.Agents
}

// SetAgents updates the Agents content (thread-safe).
func (p *Persona) SetAgents(s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Agents = s
}

// GetMemory returns the Memory content (thread-safe).
func (p *Persona) GetMemory() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.Memory
}

// SetMemory updates the Memory content (thread-safe).
func (p *Persona) SetMemory(s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Memory = s
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
	MaxToolIter     int           // max tool-calling iterations per turn (default: 15)
	MaxOutputTokens int           // max tokens in LLM response (default: 4096)
	Temperature     *float64      // LLM sampling temperature (nil = provider default)
	ReasoningEffort string        // reasoning effort for OpenAI models: "low", "medium", "high", "xhigh" (default: "")

	ShellTimeout    time.Duration // shell command timeout (default: 30s)

	// Context management
	ContextStrategy string // "anchor", "masking", "hybrid" (default: "masking")

	// Executor
	Executor string // "local", "noop" (default: "local")

	// Storage
	TapeDir      string // directory for tape JSONL files (default: ~/.golem/tapes)
	WorkspaceDir string // agent workspace root (default: CWD for CLI, ~/.golem/agents/<name>/workspace for background)
	// Channels
	TelegramToken    string
	TelegramACL      []int64
	LarkAppID        string
	LarkAppSecret    string
	LarkVerifyToken  string
	LarkCallbackPort string // HTTP port for Lark card action callbacks (e.g. "9876", empty = disabled)

	// Sessions
	MaxSessions     int           // max concurrent per-chat sessions (default: 100)
	SessionIdleTime time.Duration // evict sessions idle longer than this (default: 24h)

	// Skills
	SkillReloadInterval time.Duration // re-discover skills from disk at this interval (default: 5m, 0=disabled)

	// Rate limiting
	LLMRateLimit int // max LLM API requests per second (default: 10, 0=unlimited)

	// Observability
	MetricsPort string // HTTP port for /debug/metrics (default: "", disabled)

	// Web
	WebSearchBackend string // "bing", "stub" (default: "bing")

	// Logging
	LogLevel string // "debug", "info", "warn", "error"

	// Tool access control
	ToolAllow map[string]bool // if non-empty, only these tools may execute
	ToolDeny  map[string]bool // these tools are always blocked (takes precedence)

	// Persona (three-layer identity system)
	Persona *Persona
}

// Load reads config from two distinct sources with strict boundaries:
//
// Global config (~/.golem/config.env) provides:
//   - LLM settings: model, API keys, base URLs, rate limit
//   - Web search backend
//
// Agent config (~/.golem/agents/<agentName>/config.env) provides:
//   - Agent behavior: max tool iter, shell timeout, context strategy, executor
//   - Channel credentials: Lark, Telegram
//   - Sessions, memory, tape dir, log level
//   - System prompt from system-prompt.md in the agent dir
//
// Precedence within each tier:
//  1. flagOverrides (CLI flags, only non-empty values)
//  2. Environment variables set in the shell
//  3. The tier's config.env file
//  4. Hardcoded defaults
//
// agentName selects the per-agent directory; pass "" for defaults only.
func Load(
	agentName string, flagOverrides map[string]string,
) (*Config, error) {
	globalVars := readDotenv(filepath.Join(GolemHome(), "config.env"))
	var agentVars map[string]string
	if agentName != "" {
		agentVars = readDotenv(filepath.Join(GolemHome(), "agents", agentName, "config.env"))
	}

	g := envLookup(globalVars)
	a := envLookup(agentVars)

	cfg := &Config{
		AgentName: agentName,

		// Global tier: LLM, skills, web.
		Model:            g.str("GOLEM_MODEL", "openai:gpt-4o"),
		LLMRateLimit:     g.integer("GOLEM_LLM_RATE_LIMIT", 10),
		MetricsPort:      g.str("GOLEM_METRICS_PORT", ""),
		WebSearchBackend: g.str("GOLEM_WEB_SEARCH_BACKEND", "bing"),

		// Agent tier: behavior, storage, logging.
		MaxToolIter:         a.integer("GOLEM_MAX_TOOL_ITER", 15),
		MaxOutputTokens:     a.integer("GOLEM_MAX_OUTPUT_TOKENS", 4096),
		Temperature:         a.optFloat64("GOLEM_TEMPERATURE"),
		ReasoningEffort:     a.str("GOLEM_REASONING_EFFORT", "high"),
		ShellTimeout:        a.duration("GOLEM_SHELL_TIMEOUT", 30*time.Second),
		ContextStrategy:     a.str("GOLEM_CONTEXT_STRATEGY", "masking"),
		Executor:            a.str("GOLEM_EXECUTOR", "local"),
		TapeDir:             expandHome(a.str("GOLEM_TAPE_DIR", "~/.golem/tapes")),
		WorkspaceDir:        expandHome(a.str("GOLEM_WORKSPACE_DIR", "")),
		LogLevel:            a.str("GOLEM_LOG_LEVEL", "info"),
		MaxSessions:         a.integer("GOLEM_MAX_SESSIONS", 100),
		SessionIdleTime:     a.duration("GOLEM_SESSION_IDLE_TIME", 24*time.Hour),
		SkillReloadInterval: a.duration("GOLEM_SKILL_RELOAD_INTERVAL", 5*time.Minute),

		// Agent tier: channels.
		TelegramToken:    a.str("TELEGRAM_BOT_TOKEN", ""),
		TelegramACL:      a.int64List("TELEGRAM_ALLOW_FROM"),
		LarkAppID:        a.str("LARK_APP_ID", ""),
		LarkAppSecret:    a.str("LARK_APP_SECRET", ""),
		LarkVerifyToken:  a.str("LARK_VERIFY_TOKEN", ""),
		LarkCallbackPort: a.str("LARK_CALLBACK_PORT", ""),
	}

	// Tool access control.
	cfg.ToolAllow = parseToolSet(a.str("GOLEM_TOOL_ALLOW", ""))
	cfg.ToolDeny = parseToolSet(a.str("GOLEM_TOOL_DENY", ""))

	// Collect API keys and base URLs from shell env + global config.
	cfg.APIKeys = make(map[string]string)
	cfg.BaseURLs = make(map[string]string)
	collectProviderKeys(cfg, globalVars)

	applyFlagOverrides(cfg, flagOverrides)

	// Default workspace directory: CWD for CLI agents, per-agent dir for background agents.
	if cfg.WorkspaceDir == "" {
		if agentName == "" || agentName == "default" {
			cfg.WorkspaceDir, _ = os.Getwd()
		} else {
			cfg.WorkspaceDir = filepath.Join(GolemHome(), "agents", agentName, "workspace")
		}
	}

	// Load persona files (three-layer identity system).
	cfg.Persona = loadPersona(agentName)

	// Load per-agent system prompt if present (fallback when no persona).
	if agentName != "" && !cfg.Persona.HasPersona() {
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
	if c.Temperature != nil && (*c.Temperature < 0 || *c.Temperature > 2) {
		return fmt.Errorf("temperature must be between 0 and 2, got %v", *c.Temperature)
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
	if c.LLMRateLimit <= 0 {
		return fmt.Errorf("LLM rate limit must be positive, got %d", c.LLMRateLimit)
	}
	if c.MaxSessions <= 0 {
		return fmt.Errorf("max sessions must be positive, got %d", c.MaxSessions)
	}
	if c.SessionIdleTime <= 0 {
		return fmt.Errorf("session idle time must be positive, got %v", c.SessionIdleTime)
	}
	validStrategies := map[string]bool{"anchor": true, "masking": true, "hybrid": true}
	if !validStrategies[c.ContextStrategy] {
		return fmt.Errorf("invalid context strategy %q: must be one of anchor, masking, hybrid", c.ContextStrategy)
	}
	return nil
}

// readDotenv reads a .env file into a map without setting env vars.
// Returns an empty map if the file does not exist or cannot be read.
func readDotenv(path string) map[string]string {
	m, err := godotenv.Read(path)
	if err != nil {
		return make(map[string]string)
	}
	return m
}

// envLookup provides typed lookups against a dotenv map,
// with shell environment variables taking precedence.
type envLookup map[string]string

func (m envLookup) get(key string) (string, bool) {
	if v := os.Getenv(key); v != "" {
		return v, true
	}
	if v, ok := m[key]; ok && v != "" {
		return v, true
	}
	return "", false
}

func (m envLookup) str(key, defaultVal string) string {
	if v, ok := m.get(key); ok {
		return v
	}
	return defaultVal
}

func (m envLookup) integer(key string, defaultVal int) int {
	if v, ok := m.get(key); ok {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return defaultVal
}

func (m envLookup) optFloat64(key string) *float64 {
	if v, ok := m.get(key); ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return &f
		}
	}
	return nil
}

func (m envLookup) duration(
	key string, defaultVal time.Duration,
) time.Duration {
	if v, ok := m.get(key); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return defaultVal
}

func (m envLookup) int64List(key string) []int64 {
	v, ok := m.get(key)
	if !ok {
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

// collectProviderKeys scans shell env and globalVars for API keys and base URLs.
// Pattern: <PROVIDER>_API_KEY and <PROVIDER>_BASE_URL.
func collectProviderKeys(
	cfg *Config, globalVars map[string]string,
) {
	// Shell environment first (higher precedence).
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
	// Global config file (lower precedence — don't overwrite shell env).
	for key, val := range globalVars {
		if val == "" {
			continue
		}
		if suffix, found := strings.CutSuffix(key, "_API_KEY"); found && suffix != "" {
			lower := strings.ToLower(suffix)
			if _, exists := cfg.APIKeys[lower]; !exists {
				cfg.APIKeys[lower] = val
			}
		}
		if suffix, found := strings.CutSuffix(key, "_BASE_URL"); found && suffix != "" {
			lower := strings.ToLower(suffix)
			if _, exists := cfg.BaseURLs[lower]; !exists {
				cfg.BaseURLs[lower] = val
			}
		}
	}
}

// applyFlagOverrides applies CLI flag overrides (highest precedence).
func applyFlagOverrides(
	cfg *Config, flags map[string]string,
) {
	if flags == nil {
		return
	}
	overrides := map[string]*string{
		"model":            &cfg.Model,
		"tape-dir":         &cfg.TapeDir,
		"workspace-dir":    &cfg.WorkspaceDir,
		"log-level":        &cfg.LogLevel,
		"context-strategy": &cfg.ContextStrategy,
		"executor":         &cfg.Executor,
	}
	for key, ptr := range overrides {
		if v, ok := flags[key]; ok && v != "" {
			*ptr = v
		}
	}
	// tape-dir and workspace-dir need home expansion.
	if v, ok := flags["tape-dir"]; ok && v != "" {
		cfg.TapeDir = expandHome(v)
	}
	if v, ok := flags["workspace-dir"]; ok && v != "" {
		cfg.WorkspaceDir = expandHome(v)
	}
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

// loadPersona reads the three-layer persona files for an agent.
// USER.md is read from the global ~/.golem/ directory (shared by all agents).
// All other files are read from ~/.golem/agents/<name>/.
// Returns a Persona with whatever files exist; missing files are empty strings.
func loadPersona(agentName string) *Persona {
	p := &Persona{}

	// Layer 1: USER.md is global.
	if data, err := os.ReadFile(filepath.Join(GolemHome(), "USER.md")); err == nil {
		p.User = strings.TrimSpace(string(data))
	}

	if agentName == "" {
		return p
	}

	agentDir := filepath.Join(GolemHome(), "agents", agentName)

	// Layer 1: SOUL.md (agent-specific).
	p.SoulPath = filepath.Join(agentDir, "SOUL.md")
	if data, err := os.ReadFile(p.SoulPath); err == nil {
		p.Soul = strings.TrimSpace(string(data))
	}

	// Layer 2: AGENTS.md.
	p.AgentsPath = filepath.Join(agentDir, "AGENTS.md")
	if data, err := os.ReadFile(p.AgentsPath); err == nil {
		p.Agents = strings.TrimSpace(string(data))
	}

	// Layer 3: MEMORY.md.
	p.MemoryPath = filepath.Join(agentDir, "MEMORY.md")
	if data, err := os.ReadFile(p.MemoryPath); err == nil {
		p.Memory = strings.TrimSpace(string(data))
	}

	return p
}

// parseToolSet parses a comma-separated list of tool names into a set.
// Returns nil for empty input.
func parseToolSet(s string) map[string]bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	set := make(map[string]bool)
	for _, name := range strings.Split(s, ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			set[name] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	return set
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
