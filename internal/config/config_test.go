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
func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"GOLEM_MODEL", "GOLEM_MAX_TOOL_ITER", "GOLEM_SHELL_TIMEOUT",
		"GOLEM_CONTEXT_STRATEGY", "GOLEM_EXECUTOR", "GOLEM_TAPE_DIR",
		"GOLEM_SKILLS_DIR", "GOLEM_LOG_LEVEL",
		"OPENAI_API_KEY", "ANTHROPIC_API_KEY",
		"OPENAI_BASE_URL", "ANTHROPIC_BASE_URL",
		"TELEGRAM_BOT_TOKEN", "TELEGRAM_ALLOW_FROM",
		"LARK_APP_ID", "LARK_APP_SECRET", "LARK_VERIFY_TOKEN",
		"MNEMOS_URL", "MNEMOS_SPACE_ID",
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

	// Create a temporary agent directory with config.env and system-prompt.md.
	agentDir := t.TempDir()
	t.Setenv("HOME", agentDir) // GolemHome() uses ~ expansion

	golemAgentDir := filepath.Join(agentDir, ".golem", "agents", "test-bot")
	if err := os.MkdirAll(golemAgentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(golemAgentDir, "config.env"), []byte("GOLEM_MODEL=anthropic:claude-sonnet-4-20250514\nLARK_APP_ID=lark-test-123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(golemAgentDir, "system-prompt.md"), []byte("You are a code reviewer."), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load("test-bot", nil)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.AgentName != "test-bot" {
		t.Errorf("AgentName = %q, want %q", cfg.AgentName, "test-bot")
	}
	if cfg.Model != "anthropic:claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want %q", cfg.Model, "anthropic:claude-sonnet-4-20250514")
	}
	if cfg.LarkAppID != "lark-test-123" {
		t.Errorf("LarkAppID = %q, want %q", cfg.LarkAppID, "lark-test-123")
	}
	if cfg.SystemPrompt != "You are a code reviewer." {
		t.Errorf("SystemPrompt = %q, want %q", cfg.SystemPrompt, "You are a code reviewer.")
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
