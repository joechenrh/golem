package router

import (
	"testing"
)

// ─── RouteUser Tests ─────────────────────────────────────────────

func TestRouteUser_InternalCommands(t *testing.T) {
	tests := []struct {
		input       string
		wantCommand string
		wantArgs    string
		wantKind    CommandKind
	}{
		{":help", "help", "", CommandInternal},
		{":quit", "quit", "", CommandInternal},
		{":tape.info", "tape.info", "", CommandInternal},
		{":tape.search hello", "tape.search", "hello", CommandInternal},
		{":tools", "tools", "", CommandInternal},
		{":skills", "skills", "", CommandInternal},
		{":model openai:gpt-4o", "model", "openai:gpt-4o", CommandInternal},
		{":reset context-reset", "reset", "context-reset", CommandInternal},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := RouteUser(tt.input)
			if !result.IsCommand {
				t.Fatal("expected IsCommand=true")
			}
			if result.Command != tt.wantCommand {
				t.Errorf("Command = %q, want %q", result.Command, tt.wantCommand)
			}
			if result.Args != tt.wantArgs {
				t.Errorf("Args = %q, want %q", result.Args, tt.wantArgs)
			}
			if result.Kind != tt.wantKind {
				t.Errorf("Kind = %d, want %d", result.Kind, tt.wantKind)
			}
		})
	}
}

func TestRouteUser_ShellCommands(t *testing.T) {
	tests := []struct {
		input       string
		wantCommand string
	}{
		{":git status", "git status"},
		{":ls -la", "ls -la"},
		{":echo hello world", "echo hello world"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := RouteUser(tt.input)
			if !result.IsCommand {
				t.Fatal("expected IsCommand=true")
			}
			if result.Kind != CommandShell {
				t.Errorf("Kind = %d, want CommandShell", result.Kind)
			}
			if result.Command != tt.wantCommand {
				t.Errorf("Command = %q, want %q", result.Command, tt.wantCommand)
			}
		})
	}
}

func TestRouteUser_NotCommand(t *testing.T) {
	tests := []string{
		"What is Go?",
		"Hello there",
		"",
		"   ",
		"no comma prefix",
	}

	for _, input := range tests {
		result := RouteUser(input)
		if result.IsCommand {
			t.Errorf("RouteUser(%q) = IsCommand=true, want false", input)
		}
	}
}

func TestRouteUser_BareColon(t *testing.T) {
	result := RouteUser(":")
	if result.IsCommand {
		t.Error("bare colon should not be a command")
	}
}

func TestRouteUser_WhitespaceHandling(t *testing.T) {
	result := RouteUser("  :help  ")
	if !result.IsCommand {
		t.Fatal("expected IsCommand=true with leading/trailing whitespace")
	}
	if result.Command != "help" {
		t.Errorf("Command = %q, want %q", result.Command, "help")
	}
}

// ─── RouteAssistant Tests ────────────────────────────────────────

func TestRouteAssistant_DetectsCommands(t *testing.T) {
	output := "Here is some text\n:git log\nmore text"
	commands, cleanText := RouteAssistant(output)

	if len(commands) != 1 {
		t.Fatalf("len(commands) = %d, want 1", len(commands))
	}
	if commands[0].Command != "git log" {
		t.Errorf("Command = %q, want %q", commands[0].Command, "git log")
	}
	if commands[0].Kind != CommandShell {
		t.Errorf("Kind = %d, want CommandShell", commands[0].Kind)
	}
	if commands[0].Line != 1 {
		t.Errorf("Line = %d, want 1", commands[0].Line)
	}

	// Clean text should not contain the command line.
	if cleanText != "Here is some text\nmore text" {
		t.Errorf("cleanText = %q", cleanText)
	}
}

func TestRouteAssistant_SkipsCodeFences(t *testing.T) {
	output := "Here's code:\n```\n:ls\n```\n:git log"
	commands, _ := RouteAssistant(output)

	if len(commands) != 1 {
		t.Fatalf("len(commands) = %d, want 1", len(commands))
	}
	if commands[0].Command != "git log" {
		t.Errorf("Command = %q, want %q (should skip :ls inside fence)", commands[0].Command, "git log")
	}
}

func TestRouteAssistant_MultipleCodeFences(t *testing.T) {
	output := "text\n```\n:inside1\n```\nmiddle\n```\n:inside2\n```\n:outside"
	commands, _ := RouteAssistant(output)

	if len(commands) != 1 {
		t.Fatalf("len(commands) = %d, want 1", len(commands))
	}
	if commands[0].Command != "outside" {
		t.Errorf("Command = %q, want %q", commands[0].Command, "outside")
	}
}

func TestRouteAssistant_NoCommands(t *testing.T) {
	output := "Just regular text\nwith no commands\nat all"
	commands, cleanText := RouteAssistant(output)

	if len(commands) != 0 {
		t.Errorf("len(commands) = %d, want 0", len(commands))
	}
	if cleanText != output {
		t.Errorf("cleanText should equal original output")
	}
}

func TestRouteAssistant_InternalCommand(t *testing.T) {
	output := "Let me check\n:tape.info\nDone"
	commands, _ := RouteAssistant(output)

	if len(commands) != 1 {
		t.Fatalf("len(commands) = %d, want 1", len(commands))
	}
	if commands[0].Command != "tape.info" {
		t.Errorf("Command = %q, want %q", commands[0].Command, "tape.info")
	}
	if commands[0].Kind != CommandInternal {
		t.Errorf("Kind = %d, want CommandInternal", commands[0].Kind)
	}
}

func TestRouteAssistant_CodeFenceWithLang(t *testing.T) {
	output := "```go\n:help\n```\n:tools"
	commands, _ := RouteAssistant(output)

	if len(commands) != 1 {
		t.Fatalf("len(commands) = %d, want 1", len(commands))
	}
	if commands[0].Command != "tools" {
		t.Errorf("Command = %q, want %q", commands[0].Command, "tools")
	}
}

