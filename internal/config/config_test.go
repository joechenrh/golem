package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	// Clear any env vars that might interfere
	os.Unsetenv("GOLEM_MODEL")
	os.Unsetenv("GOLEM_MAX_TOOL_ITER")
	os.Unsetenv("GOLEM_SHELL_TIMEOUT")
	os.Unsetenv("GOLEM_CONTEXT_STRATEGY")
	os.Unsetenv("GOLEM_EXECUTOR")

	cfg, err := Load(nil)
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
	os.Setenv("GOLEM_MODEL", "anthropic:claude-sonnet-4-20250514")
	os.Setenv("OPENAI_API_KEY", "sk-test-123")
	os.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-456")
	defer func() {
		os.Unsetenv("GOLEM_MODEL")
		os.Unsetenv("OPENAI_API_KEY")
		os.Unsetenv("ANTHROPIC_API_KEY")
	}()

	cfg, err := Load(nil)
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
	os.Setenv("GOLEM_MODEL", "openai:gpt-4o")
	defer os.Unsetenv("GOLEM_MODEL")

	flags := map[string]string{
		"model":     "anthropic:claude-sonnet-4-20250514",
		"log-level": "debug",
	}

	cfg, err := Load(flags)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	// Flags override env
	if cfg.Model != "anthropic:claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want anthropic:claude-sonnet-4-20250514", cfg.Model)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.LogLevel)
	}
}

func TestModelProvider(t *testing.T) {
	tests := []struct {
		model        string
		wantProvider string
		wantModel    string
	}{
		{"openai:gpt-4o", "openai", "gpt-4o"},
		{"anthropic:claude-sonnet-4-20250514", "anthropic", "claude-sonnet-4-20250514"},
		{"gpt-4o", "openai", "gpt-4o"}, // no prefix defaults to openai
	}

	for _, tt := range tests {
		cfg := &Config{Model: tt.model}
		provider, model := cfg.ModelProvider()
		if provider != tt.wantProvider || model != tt.wantModel {
			t.Errorf("ModelProvider(%q) = (%q, %q), want (%q, %q)",
				tt.model, provider, model, tt.wantProvider, tt.wantModel)
		}
	}
}

func TestTelegramACL(t *testing.T) {
	os.Setenv("TELEGRAM_ALLOW_FROM", "123, 456, 789")
	defer os.Unsetenv("TELEGRAM_ALLOW_FROM")

	cfg, err := Load(nil)
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

func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()

	tests := []struct {
		input string
		want  string
	}{
		{"~/foo", home + "/foo"},
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
