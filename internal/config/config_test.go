package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// clearConfigEnv sets all config-related env vars to empty via t.Setenv,
// ensuring a clean slate and automatic restoration after the test.
// Also redirects HOME to a temp dir so no real config files are loaded.
func clearConfigEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	for _, key := range []string{
		"GOLEM_MODEL", "GOLEM_MAX_TOOL_ITER", "GOLEM_SHELL_TIMEOUT",
		"GOLEM_CONTEXT_STRATEGY", "GOLEM_EXECUTOR", "GOLEM_TAPE_DIR",
		"GOLEM_SKILLS_DIR", "GOLEM_LOG_LEVEL",
		"GOLEM_LLM_RATE_LIMIT", "GOLEM_WEB_SEARCH_BACKEND",
		"OPENAI_API_KEY", "ANTHROPIC_API_KEY",
		"OPENAI_BASE_URL", "ANTHROPIC_BASE_URL",
		"TELEGRAM_BOT_TOKEN", "TELEGRAM_ALLOW_FROM",
		"LARK_APP_ID", "LARK_APP_SECRET", "LARK_VERIFY_TOKEN",
		"MNEMO_DB_HOST", "MNEMO_DB_USER", "MNEMO_DB_PASS",
		"MNEMO_DB_NAME", "MNEMO_AUTO_EMBED_MODEL", "MNEMO_AUTO_EMBED_DIMS",
	} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
}

func TestLoadDefaults(t *testing.T) {
	clearConfigEnv(t)

	cfg, err := Load("", nil)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Model != "openai:gpt-4o" {
		t.Errorf("Model = %q, want %q", cfg.Model, "openai:gpt-4o")
	}
	if cfg.MaxToolIter != 15 {
		t.Errorf("MaxToolIter = %d, want 15", cfg.MaxToolIter)
	}
	if cfg.ShellTimeout != 30*time.Second {
		t.Errorf("ShellTimeout = %v, want 30s", cfg.ShellTimeout)
	}
	if cfg.ContextStrategy != "masking" {
		t.Errorf("ContextStrategy = %q, want %q", cfg.ContextStrategy, "masking")
	}
	if cfg.Executor != "local" {
		t.Errorf("Executor = %q, want %q", cfg.Executor, "local")
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "info")
	}
}

func TestLoadEnvOverride(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("GOLEM_MODEL", "anthropic:claude-sonnet-4-20250514")
	t.Setenv("OPENAI_API_KEY", "sk-test-123")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-456")

	cfg, err := Load("", nil)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Model != "anthropic:claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want anthropic:claude-sonnet-4-20250514", cfg.Model)
	}
	if cfg.APIKeys["openai"] != "sk-test-123" {
		t.Errorf("APIKeys[openai] = %q, want sk-test-123", cfg.APIKeys["openai"])
	}
	if cfg.APIKeys["anthropic"] != "sk-ant-test-456" {
		t.Errorf("APIKeys[anthropic] = %q, want sk-ant-test-456", cfg.APIKeys["anthropic"])
	}
}

func TestLoadFlagOverride(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("GOLEM_MODEL", "openai:gpt-4o")

	flags := map[string]string{
		"model":     "anthropic:claude-sonnet-4-20250514",
		"log-level": "debug",
	}

	cfg, err := Load("", flags)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Model != "anthropic:claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want anthropic:claude-sonnet-4-20250514", cfg.Model)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.LogLevel)
	}
}

func TestTelegramACL(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("TELEGRAM_ALLOW_FROM", "123, 456, 789")

	cfg, err := Load("", nil)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	want := []int64{123, 456, 789}
	if len(cfg.TelegramACL) != len(want) {
		t.Fatalf("TelegramACL len = %d, want %d", len(cfg.TelegramACL), len(want))
	}
	for i, v := range want {
		if cfg.TelegramACL[i] != v {
			t.Errorf("TelegramACL[%d] = %d, want %d", i, cfg.TelegramACL[i], v)
		}
	}
}

func TestValidationInvalidLogLevel(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("GOLEM_LOG_LEVEL", "trace")

	_, err := Load("", nil)
	if err == nil {
		t.Fatal("expected error for invalid log level, got nil")
	}
	if !strings.Contains(err.Error(), "log level") {
		t.Errorf("error = %q, want mention of log level", err.Error())
	}
}

func TestValidationInvalidMaxToolIter(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("GOLEM_MAX_TOOL_ITER", "0")

	_, err := Load("", nil)
	if err == nil {
		t.Fatal("expected error for zero max tool iter, got nil")
	}
	if !strings.Contains(err.Error(), "tool iterations") {
		t.Errorf("error = %q, want mention of tool iterations", err.Error())
	}
}

func TestValidationInvalidModel(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("GOLEM_MODEL", "a:b:c")

	_, err := Load("", nil)
	if err == nil {
		t.Fatal("expected error for invalid model format, got nil")
	}
	if !strings.Contains(err.Error(), "model format") {
		t.Errorf("error = %q, want mention of model format", err.Error())
	}
}

func TestLoadBaseURLs(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("OPENAI_BASE_URL", "https://my-proxy.example.com/v1")
	t.Setenv("ANTHROPIC_BASE_URL", "https://my-claude-proxy.example.com")

	cfg, err := Load("", nil)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.BaseURLs["openai"] != "https://my-proxy.example.com/v1" {
		t.Errorf("BaseURLs[openai] = %q, want https://my-proxy.example.com/v1", cfg.BaseURLs["openai"])
	}
	if cfg.BaseURLs["anthropic"] != "https://my-claude-proxy.example.com" {
		t.Errorf("BaseURLs[anthropic] = %q, want https://my-claude-proxy.example.com", cfg.BaseURLs["anthropic"])
	}
}

func TestLoadBaseURLsEmpty(t *testing.T) {
	clearConfigEnv(t)

	cfg, err := Load("", nil)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if len(cfg.BaseURLs) != 0 {
		t.Errorf("BaseURLs = %v, want empty map", cfg.BaseURLs)
	}
}

func TestLoadAgentConfig(t *testing.T) {
	clearConfigEnv(t)

	// Create temp HOME with global + agent config files.
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Global config: LLM settings.
	golemDir := filepath.Join(home, ".golem")
	if err := os.MkdirAll(golemDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(golemDir, "config.env"), []byte("GOLEM_MODEL=anthropic:claude-sonnet-4-20250514\nOPENAI_API_KEY=sk-global-123\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Agent config: channel + behavior settings.
	agentDir := filepath.Join(golemDir, "agents", "test-bot")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "config.env"), []byte("LARK_APP_ID=lark-test-123\nGOLEM_MAX_TOOL_ITER=25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "system-prompt.md"), []byte("You are a code reviewer."), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load("test-bot", nil)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.AgentName != "test-bot" {
		t.Errorf("AgentName = %q, want %q", cfg.AgentName, "test-bot")
	}
	// Model comes from global config.
	if cfg.Model != "anthropic:claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want %q", cfg.Model, "anthropic:claude-sonnet-4-20250514")
	}
	// API key comes from global config.
	if cfg.APIKeys["openai"] != "sk-global-123" {
		t.Errorf("APIKeys[openai] = %q, want %q", cfg.APIKeys["openai"], "sk-global-123")
	}
	// Lark comes from agent config.
	if cfg.LarkAppID != "lark-test-123" {
		t.Errorf("LarkAppID = %q, want %q", cfg.LarkAppID, "lark-test-123")
	}
	// MaxToolIter comes from agent config.
	if cfg.MaxToolIter != 25 {
		t.Errorf("MaxToolIter = %d, want 25", cfg.MaxToolIter)
	}
	if cfg.SystemPrompt != "You are a code reviewer." {
		t.Errorf("SystemPrompt = %q, want %q", cfg.SystemPrompt, "You are a code reviewer.")
	}
}

func TestSourceIsolation(t *testing.T) {
	clearConfigEnv(t)

	home := t.TempDir()
	t.Setenv("HOME", home)

	golemDir := filepath.Join(home, ".golem")
	if err := os.MkdirAll(golemDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Put agent-tier keys in global config — they should NOT be picked up
	// for agent-tier fields.
	if err := os.WriteFile(filepath.Join(golemDir, "config.env"),
		[]byte("LARK_APP_ID=should-not-appear\nGOLEM_MAX_TOOL_ITER=99\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Put global-tier keys in agent config — they should NOT affect
	// global-tier fields.
	agentDir := filepath.Join(golemDir, "agents", "iso-bot")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "config.env"),
		[]byte("GOLEM_MODEL=should-not-appear\nOPENAI_API_KEY=sk-should-not-appear\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load("iso-bot", nil)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	// Global-tier: model should NOT come from agent config.
	if cfg.Model == "should-not-appear" {
		t.Error("Model was read from agent config — should only come from global")
	}
	// Global-tier: API key should NOT come from agent config.
	if cfg.APIKeys["openai"] == "sk-should-not-appear" {
		t.Error("APIKeys[openai] was read from agent config — should only come from global")
	}
	// Agent-tier: Lark should NOT come from global config.
	if cfg.LarkAppID == "should-not-appear" {
		t.Error("LarkAppID was read from global config — should only come from agent")
	}
	// Agent-tier: MaxToolIter should NOT come from global config.
	if cfg.MaxToolIter == 99 {
		t.Error("MaxToolIter was read from global config — should only come from agent")
	}
}

func TestHasRemoteChannels(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{
			name: "no credentials",
			cfg:  Config{},
			want: false,
		},
		{
			name: "lark credentials",
			cfg:  Config{LarkAppID: "app-id", LarkAppSecret: "secret"},
			want: true,
		},
		{
			name: "lark app id only",
			cfg:  Config{LarkAppID: "app-id"},
			want: false,
		},
		{
			name: "telegram token",
			cfg:  Config{TelegramToken: "bot-token"},
			want: true,
		},
		{
			name: "both lark and telegram",
			cfg:  Config{LarkAppID: "app-id", LarkAppSecret: "secret", TelegramToken: "bot-token"},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.HasRemoteChannels(); got != tt.want {
				t.Errorf("HasRemoteChannels() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDiscoverAgents(t *testing.T) {
	// Use a temp directory as HOME so GolemHome() points there.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	t.Run("no agents dir", func(t *testing.T) {
		names, err := DiscoverAgents()
		if err != nil {
			t.Fatalf("DiscoverAgents() error: %v", err)
		}
		if names != nil {
			t.Errorf("DiscoverAgents() = %v, want nil", names)
		}
	})

	t.Run("with agent subdirs", func(t *testing.T) {
		agentsDir := filepath.Join(tmpHome, ".golem", "agents")
		for _, name := range []string{"default", "lark-bot", "telegram-bot"} {
			if err := os.MkdirAll(filepath.Join(agentsDir, name), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		// Create a regular file that should be ignored.
		if err := os.WriteFile(filepath.Join(agentsDir, "not-a-dir.txt"), []byte("hi"), 0o644); err != nil {
			t.Fatal(err)
		}

		names, err := DiscoverAgents()
		if err != nil {
			t.Fatalf("DiscoverAgents() error: %v", err)
		}

		want := map[string]bool{"default": true, "lark-bot": true, "telegram-bot": true}
		if len(names) != len(want) {
			t.Fatalf("DiscoverAgents() returned %d names, want %d: %v", len(names), len(want), names)
		}
		for _, n := range names {
			if !want[n] {
				t.Errorf("unexpected agent name %q", n)
			}
		}
	})
}

func TestLoadPersona(t *testing.T) {
	clearConfigEnv(t)

	home := t.TempDir()
	t.Setenv("HOME", home)
	golemDir := filepath.Join(home, ".golem")

	// Global USER.md.
	os.MkdirAll(golemDir, 0o755)
	os.WriteFile(filepath.Join(golemDir, "USER.md"), []byte("Name: Alice\nTimezone: UTC"), 0o644)

	// Agent persona files.
	agentDir := filepath.Join(golemDir, "agents", "persona-bot")
	os.MkdirAll(agentDir, 0o755)
	os.WriteFile(filepath.Join(agentDir, "config.env"), []byte(""), 0o644)
	os.WriteFile(filepath.Join(agentDir, "SOUL.md"), []byte("You are a research assistant."), 0o644)
	os.WriteFile(filepath.Join(agentDir, "IDENTITY.md"), []byte("Name: Dwight\nEmoji: magnifier"), 0o644)
	os.WriteFile(filepath.Join(agentDir, "AGENTS.md"), []byte("Always cite sources."), 0o644)
	os.WriteFile(filepath.Join(agentDir, "MEMORY.md"), []byte("User prefers short answers."), 0o644)

	cfg, err := Load("persona-bot", nil)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	p := cfg.Persona
	if !p.HasPersona() {
		t.Fatal("HasPersona() = false, want true")
	}
	if p.Soul != "You are a research assistant." {
		t.Errorf("Soul = %q", p.Soul)
	}
	if p.Identity != "Name: Dwight\nEmoji: magnifier" {
		t.Errorf("Identity = %q", p.Identity)
	}
	if p.User != "Name: Alice\nTimezone: UTC" {
		t.Errorf("User = %q", p.User)
	}
	if p.Agents != "Always cite sources." {
		t.Errorf("Agents = %q", p.Agents)
	}
	if p.Memory != "User prefers short answers." {
		t.Errorf("Memory = %q", p.Memory)
	}
	if p.MemoryPath != filepath.Join(agentDir, "MEMORY.md") {
		t.Errorf("MemoryPath = %q", p.MemoryPath)
	}
	// system-prompt.md should NOT be loaded when persona exists.
	if cfg.SystemPrompt != "" {
		t.Errorf("SystemPrompt = %q, want empty (persona takes precedence)", cfg.SystemPrompt)
	}
}

func TestLoadPersonaFallback(t *testing.T) {
	clearConfigEnv(t)

	home := t.TempDir()
	t.Setenv("HOME", home)

	// Agent with system-prompt.md but no SOUL.md.
	agentDir := filepath.Join(home, ".golem", "agents", "flat-bot")
	os.MkdirAll(agentDir, 0o755)
	os.WriteFile(filepath.Join(agentDir, "config.env"), []byte(""), 0o644)
	os.WriteFile(filepath.Join(agentDir, "system-prompt.md"), []byte("You are a flat bot."), 0o644)

	cfg, err := Load("flat-bot", nil)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Persona.HasPersona() {
		t.Error("HasPersona() = true, want false (no SOUL.md)")
	}
	if cfg.SystemPrompt != "You are a flat bot." {
		t.Errorf("SystemPrompt = %q, want fallback", cfg.SystemPrompt)
	}
}

func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()

	tests := []struct {
		input string
		want  string
	}{
		{"~/foo", home + "/foo"},
		{"~", home},
		{"/absolute/path", "/absolute/path"},
		{"relative/path", "relative/path"},
	}

	for _, tt := range tests {
		got := expandHome(tt.input)
		if got != tt.want {
			t.Errorf("expandHome(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
