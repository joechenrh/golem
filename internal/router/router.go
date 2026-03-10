package router

import (
	"strings"
)

// CommandKind distinguishes internal commands from shell pass-through.
type CommandKind int

const (
	CommandInternal CommandKind = iota // :help, :tape.info, :tools, :quit, :model
	CommandShell                       // :git status, :ls -la
	CommandToolExec                    // ,read_file path=test.txt
)

// internalCommands is the set of recognized internal command names.
var internalCommands = map[string]bool{
	"help":        true,
	"quit":        true,
	"usage":       true,
	"metrics":     true,
	"tape.info":   true,
	"tape.search": true,
	"tools":       true,
	"skills":      true,
	"model":       true,
	"reset":       true,
}

// RouteResult is the outcome of routing user input.
type RouteResult struct {
	IsCommand bool
	Command   string      // command name (e.g., "help", "tape.info", "git status")
	Args      string      // everything after the command name
	Kind      CommandKind // Internal or Shell
}

// RouteUser classifies user input.
// Lines starting with ":" are colon-commands; lines starting with ","
// are comma-commands (direct tool execution); everything else goes to the LLM.
func RouteUser(input string) RouteResult {
	input = strings.TrimSpace(input)

	// Comma-command: direct tool execution bypassing the LLM.
	if strings.HasPrefix(input, ",") {
		rest := input[1:]
		if rest == "" {
			return RouteResult{}
		}
		toolName, rawArgs, _ := strings.Cut(rest, " ")
		return RouteResult{
			IsCommand: true,
			Command:   toolName,
			Args:      strings.TrimSpace(rawArgs),
			Kind:      CommandToolExec,
		}
	}

	if !strings.HasPrefix(input, ":") {
		return RouteResult{}
	}

	// Strip the colon prefix.
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

// DetectedCommand represents a colon command found in assistant output.
type DetectedCommand struct {
	Command string
	Args    string
	Kind    CommandKind
	Line    int // 0-indexed line number where the command was found
}

// RouteAssistant scans assistant output for colon commands at line starts.
// Skips commands inside code fences (``` blocks).
// Returns detected commands and the cleaned text (command lines removed).
func RouteAssistant(
	output string,
) (commands []DetectedCommand, cleanText string) {
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

		// Check for colon command at line start.
		if strings.HasPrefix(trimmed, ":") && len(trimmed) > 1 {
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
