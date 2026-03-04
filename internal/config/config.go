package config

import (
	"errors"
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

// Config holds all configuration for the golem agent.
type Config struct {
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
	LarkWebhookPort int

	// Memory
	MnemosURL     string
	MnemosSpaceID string

	// Logging
	LogLevel string // "debug", "info", "warn", "error"
}

// Load reads config from all sources with the following precedence:
//
//  1. flagOverrides (CLI flags, only non-empty values)
//  2. Environment variables
//  3. .env.local file
//  4. Hardcoded defaults
func Load(flagOverrides map[string]string) (*Config, error) {
	// Load .env.local first (lowest precedence, env vars override).
	// Ignore file-not-found but surface parse errors.
	if err := godotenv.Load(".env.local"); err != nil && !errors.Is(err, os.ErrNotExist) {
		// godotenv returns a *os.PathError for missing files
		if _, ok := err.(*os.PathError); !ok {
			return nil, fmt.Errorf("parsing .env.local: %w", err)
		}
	}

	cfg := &Config{
		Model:           getWithDefault("GOLEM_MODEL", "openai:gpt-4o"),
		APIKeys:         make(map[string]string),
		MaxToolIter:     getIntWithDefault("GOLEM_MAX_TOOL_ITER", 15),
		ShellTimeout:    getDurationWithDefault("GOLEM_SHELL_TIMEOUT", 30*time.Second),
		ContextStrategy: getWithDefault("GOLEM_CONTEXT_STRATEGY", "masking"),
		Executor:        getWithDefault("GOLEM_EXECUTOR", "local"),
		TapeDir:         expandHome(getWithDefault("GOLEM_TAPE_DIR", "~/.golem/tapes")),
		SkillsDir:       getWithDefault("GOLEM_SKILLS_DIR", ".agent/skills"),
		TelegramToken:   os.Getenv("TELEGRAM_BOT_TOKEN"),
		LarkAppID:       os.Getenv("LARK_APP_ID"),
		LarkAppSecret:   os.Getenv("LARK_APP_SECRET"),
		LarkWebhookPort: getIntWithDefault("LARK_WEBHOOK_PORT", 9999),
		MnemosURL:       os.Getenv("MNEMOS_URL"),
		MnemosSpaceID:   getWithDefault("MNEMOS_SPACE_ID", "default"),
		LogLevel:        getWithDefault("GOLEM_LOG_LEVEL", "info"),
	}

	// Collect API keys from environment
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		cfg.APIKeys["openai"] = key
	}
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		cfg.APIKeys["anthropic"] = key
	}

	// Collect base URLs from environment
	cfg.BaseURLs = make(map[string]string)
	if u := os.Getenv("OPENAI_BASE_URL"); u != "" {
		cfg.BaseURLs["openai"] = u
	}
	if u := os.Getenv("ANTHROPIC_BASE_URL"); u != "" {
		cfg.BaseURLs["anthropic"] = u
	}

	// Telegram ACL
	if acl := os.Getenv("TELEGRAM_ALLOW_FROM"); acl != "" {
		for _, s := range strings.Split(acl, ",") {
			s = strings.TrimSpace(s)
			if id, err := strconv.ParseInt(s, 10, 64); err == nil {
				cfg.TelegramACL = append(cfg.TelegramACL, id)
			}
		}
	}

	// Apply CLI flag overrides (highest precedence)
	if flagOverrides != nil {
		if v, ok := flagOverrides["model"]; ok && v != "" {
			cfg.Model = v
		}
		if v, ok := flagOverrides["tape-dir"]; ok && v != "" {
			cfg.TapeDir = expandHome(v)
		}
		if v, ok := flagOverrides["skills-dir"]; ok && v != "" {
			cfg.SkillsDir = v
		}
		if v, ok := flagOverrides["log-level"]; ok && v != "" {
			cfg.LogLevel = v
		}
		if v, ok := flagOverrides["context-strategy"]; ok && v != "" {
			cfg.ContextStrategy = v
		}
		if v, ok := flagOverrides["executor"]; ok && v != "" {
			cfg.Executor = v
		}
	}

	// Validate
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
	// Model must be non-empty and have at most one colon separator.
	if c.Model == "" {
		return fmt.Errorf("model must not be empty")
	}
	if strings.Count(c.Model, ":") > 1 {
		return fmt.Errorf("invalid model format %q: expected \"provider:model\" or \"model\"", c.Model)
	}
	return nil
}

func getWithDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func getIntWithDefault(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return defaultVal
}

func getDurationWithDefault(key string, defaultVal time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return defaultVal
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
