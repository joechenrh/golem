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
		{",help", "help", "", CommandInternal},
		{",quit", "quit", "", CommandInternal},
		{",tape.info", "tape.info", "", CommandInternal},
		{",tape.search hello", "tape.search", "hello", CommandInternal},
		{",tools", "tools", "", CommandInternal},
		{",skills", "skills", "", CommandInternal},
		{",model openai:gpt-4o", "model", "openai:gpt-4o", CommandInternal},
		{",anchor context-reset", "anchor", "context-reset", CommandInternal},
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
		{",git status", "git status"},
		{",ls -la", "ls -la"},
		{",echo hello world", "echo hello world"},
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

func TestRouteUser_BareComma(t *testing.T) {
	result := RouteUser(",")
	if result.IsCommand {
		t.Error("bare comma should not be a command")
	}
}

func TestRouteUser_WhitespaceHandling(t *testing.T) {
	result := RouteUser("  ,help  ")
	if !result.IsCommand {
		t.Fatal("expected IsCommand=true with leading/trailing whitespace")
	}
	if result.Command != "help" {
		t.Errorf("Command = %q, want %q", result.Command, "help")
	}
}

// ─── RouteAssistant Tests ────────────────────────────────────────

func TestRouteAssistant_DetectsCommands(t *testing.T) {
	output := "Here is some text\n,git log\nmore text"
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
	output := "Here's code:\n```\n,ls\n```\n,git log"
	commands, _ := RouteAssistant(output)

	if len(commands) != 1 {
		t.Fatalf("len(commands) = %d, want 1", len(commands))
	}
	if commands[0].Command != "git log" {
		t.Errorf("Command = %q, want %q (should skip ,ls inside fence)", commands[0].Command, "git log")
	}
}

func TestRouteAssistant_MultipleCodeFences(t *testing.T) {
	output := "text\n```\n,inside1\n```\nmiddle\n```\n,inside2\n```\n,outside"
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
	output := "Let me check\n,tape.info\nDone"
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
	output := "```go\n,help\n```\n,tools"
	commands, _ := RouteAssistant(output)

	if len(commands) != 1 {
		t.Fatalf("len(commands) = %d, want 1", len(commands))
	}
	if commands[0].Command != "tools" {
		t.Errorf("Command = %q, want %q", commands[0].Command, "tools")
	}
}

// ─── ParseArgs Tests ─────────────────────────────────────────────

func TestParseArgs_Positional(t *testing.T) {
	result := ParseArgs("foo bar baz")
	if len(result.Positional) != 3 {
		t.Fatalf("len(Positional) = %d, want 3", len(result.Positional))
	}
	if result.Positional[0] != "foo" || result.Positional[1] != "bar" || result.Positional[2] != "baz" {
		t.Errorf("Positional = %v", result.Positional)
	}
}

func TestParseArgs_Flags(t *testing.T) {
	result := ParseArgs("--key=value --name test")
	if result.Flags["key"] != "value" {
		t.Errorf("Flags[key] = %q, want %q", result.Flags["key"], "value")
	}
	if result.Flags["name"] != "test" {
		t.Errorf("Flags[name] = %q, want %q", result.Flags["name"], "test")
	}
}

func TestParseArgs_BoolFlags(t *testing.T) {
	result := ParseArgs("--verbose --dry-run")
	if !result.BoolFlags["verbose"] {
		t.Error("BoolFlags[verbose] should be true")
	}
	if !result.BoolFlags["dry-run"] {
		t.Error("BoolFlags[dry-run] should be true")
	}
}

func TestParseArgs_Mixed(t *testing.T) {
	// "search --limit=10 hello --verbose world"
	// search → positional, --limit=10 → flag, hello → positional,
	// --verbose world → flag (--key value form per spec)
	result := ParseArgs("search --limit=10 hello --verbose world")
	if len(result.Positional) != 2 || result.Positional[0] != "search" || result.Positional[1] != "hello" {
		t.Errorf("Positional = %v, want [search, hello]", result.Positional)
	}
	if result.Flags["limit"] != "10" {
		t.Errorf("Flags[limit] = %q, want %q", result.Flags["limit"], "10")
	}
	if result.Flags["verbose"] != "world" {
		t.Errorf("Flags[verbose] = %q, want %q", result.Flags["verbose"], "world")
	}
}

func TestParseArgs_BoolFlagAtEnd(t *testing.T) {
	// --verbose at end of args with no following value → bool flag
	result := ParseArgs("search --verbose")
	if len(result.Positional) != 1 || result.Positional[0] != "search" {
		t.Errorf("Positional = %v, want [search]", result.Positional)
	}
	if !result.BoolFlags["verbose"] {
		t.Error("BoolFlags[verbose] should be true")
	}
}

func TestParseArgs_QuotedStrings(t *testing.T) {
	result := ParseArgs(`--name "hello world" --desc 'single quoted'`)
	if result.Flags["name"] != "hello world" {
		t.Errorf("Flags[name] = %q, want %q", result.Flags["name"], "hello world")
	}
	if result.Flags["desc"] != "single quoted" {
		t.Errorf("Flags[desc] = %q, want %q", result.Flags["desc"], "single quoted")
	}
}

func TestParseArgs_Empty(t *testing.T) {
	result := ParseArgs("")
	if len(result.Positional) != 0 {
		t.Errorf("Positional = %v, want empty", result.Positional)
	}
	if len(result.Flags) != 0 {
		t.Errorf("Flags = %v, want empty", result.Flags)
	}
	if len(result.BoolFlags) != 0 {
		t.Errorf("BoolFlags = %v, want empty", result.BoolFlags)
	}
}

// ─── splitArgs Tests ─────────────────────────────────────────────

func TestSplitArgs_Basic(t *testing.T) {
	parts := splitArgs("hello world")
	if len(parts) != 2 || parts[0] != "hello" || parts[1] != "world" {
		t.Errorf("splitArgs = %v", parts)
	}
}

func TestSplitArgs_Quoted(t *testing.T) {
	parts := splitArgs(`"hello world" foo`)
	if len(parts) != 2 || parts[0] != "hello world" || parts[1] != "foo" {
		t.Errorf("splitArgs = %v", parts)
	}
}

func TestSplitArgs_MultipleSpaces(t *testing.T) {
	parts := splitArgs("a   b\tc")
	if len(parts) != 3 {
		t.Errorf("splitArgs = %v, want [a, b, c]", parts)
	}
}

func TestSplitArgs_BackslashEscape(t *testing.T) {
	parts := splitArgs(`"He said \"hello\"" world`)
	if len(parts) != 2 {
		t.Fatalf("splitArgs = %v, want 2 parts", parts)
	}
	if parts[0] != `He said "hello"` {
		t.Errorf("parts[0] = %q, want %q", parts[0], `He said "hello"`)
	}
	if parts[1] != "world" {
		t.Errorf("parts[1] = %q, want %q", parts[1], "world")
	}
}

func TestSplitArgs_EscapedBackslash(t *testing.T) {
	parts := splitArgs(`"path\\to" file`)
	if len(parts) != 2 {
		t.Fatalf("splitArgs = %v, want 2 parts", parts)
	}
	if parts[0] != `path\to` {
		t.Errorf("parts[0] = %q, want %q", parts[0], `path\to`)
	}
}
