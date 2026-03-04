package router

import (
	"strings"
)

// CommandKind distinguishes internal commands from shell pass-through.
type CommandKind int

const (
	CommandInternal CommandKind = iota // ,help, ,tape.info, ,tools, ,quit, ,model
	CommandShell                       // ,git status, ,ls -la
)

// internalCommands is the set of recognized internal command names.
var internalCommands = map[string]bool{
	"help":        true,
	"quit":        true,
	"tape.info":   true,
	"tape.search": true,
	"tools":       true,
	"skills":      true,
	"model":       true,
	"anchor":      true,
}

// RouteResult is the outcome of routing user input.
type RouteResult struct {
	IsCommand bool
	Command   string      // command name (e.g., "help", "tape.info", "git status")
	Args      string      // everything after the command name
	Kind      CommandKind // Internal or Shell
}

// RouteUser classifies user input.
// Lines starting with "," are commands; everything else goes to the LLM.
func RouteUser(input string) RouteResult {
	input = strings.TrimSpace(input)
	if !strings.HasPrefix(input, ",") {
		return RouteResult{}
	}

	// Strip the comma prefix.
	rest := input[1:]
	if rest == "" {
		return RouteResult{}
	}

	// Try to match an internal command.
	// Internal commands may have dotted names (e.g., "tape.info"), so check
	// both the first word and first two dotted words.
	for cmd := range internalCommands {
		if rest == cmd || strings.HasPrefix(rest, cmd+" ") {
			args := ""
			if len(rest) > len(cmd) {
				args = strings.TrimSpace(rest[len(cmd)+1:])
			}
			return RouteResult{
				IsCommand: true,
				Command:   cmd,
				Args:      args,
				Kind:      CommandInternal,
			}
		}
	}

	// Not an internal command — treat as shell command.
	return RouteResult{
		IsCommand: true,
		Command:   rest,
		Args:      "",
		Kind:      CommandShell,
	}
}

// DetectedCommand represents a comma command found in assistant output.
type DetectedCommand struct {
	Command string
	Args    string
	Kind    CommandKind
	Line    int // 0-indexed line number where the command was found
}

// RouteAssistant scans assistant output for comma commands at line starts.
// Skips commands inside code fences (``` blocks).
// Returns detected commands and the cleaned text (command lines removed).
func RouteAssistant(output string) (commands []DetectedCommand, cleanText string) {
	lines := strings.Split(output, "\n")
	inFence := false
	var cleanLines []string

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Track code fence boundaries.
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			cleanLines = append(cleanLines, line)
			continue
		}

		// Skip command detection inside code fences.
		if inFence {
			cleanLines = append(cleanLines, line)
			continue
		}

		// Check for comma command at line start.
		if strings.HasPrefix(trimmed, ",") && len(trimmed) > 1 {
			result := RouteUser(trimmed)
			if result.IsCommand {
				commands = append(commands, DetectedCommand{
					Command: result.Command,
					Args:    result.Args,
					Kind:    result.Kind,
					Line:    i,
				})
				// Don't include the command line in clean text.
				continue
			}
		}

		cleanLines = append(cleanLines, line)
	}

	cleanText = strings.Join(cleanLines, "\n")
	return commands, cleanText
}

// ParsedArgs holds parsed command arguments.
type ParsedArgs struct {
	Positional []string
	Flags      map[string]string // --key=value or --key value
	BoolFlags  map[string]bool   // --flag (no value)
}

// ParseArgs splits raw command arguments into positional args and flags.
func ParseArgs(raw string) ParsedArgs {
	result := ParsedArgs{
		Flags:     make(map[string]string),
		BoolFlags: make(map[string]bool),
	}

	raw = strings.TrimSpace(raw)
	if raw == "" {
		return result
	}

	parts := splitArgs(raw)

	for i := 0; i < len(parts); i++ {
		part := parts[i]

		if !strings.HasPrefix(part, "--") {
			result.Positional = append(result.Positional, part)
			continue
		}

		// Strip "--" prefix.
		flag := part[2:]
		if flag == "" {
			continue
		}

		// Check for --key=value form.
		if key, value, ok := strings.Cut(flag, "="); ok {
			result.Flags[key] = value
			continue
		}

		// Check if next part is a value (not another flag).
		if i+1 < len(parts) && !strings.HasPrefix(parts[i+1], "--") {
			result.Flags[flag] = parts[i+1]
			i++
			continue
		}

		// Bool flag (no value).
		result.BoolFlags[flag] = true
	}

	return result
}

// splitArgs splits a string by whitespace, respecting quoted strings and
// backslash escapes (e.g., \" inside double-quoted strings).
func splitArgs(s string) []string {
	var parts []string
	var current strings.Builder
	inQuote := false
	quoteChar := byte(0)

	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case inQuote:
			if ch == '\\' && i+1 < len(s) {
				next := s[i+1]
				// Inside quotes, backslash escapes the quote char and backslash itself.
				if next == quoteChar || next == '\\' {
					current.WriteByte(next)
					i++
					continue
				}
			}
			if ch == quoteChar {
				inQuote = false
			} else {
				current.WriteByte(ch)
			}
		case ch == '"' || ch == '\'':
			inQuote = true
			quoteChar = ch
		case ch == ' ' || ch == '\t':
			if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(ch)
		}
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}

	return parts
}
