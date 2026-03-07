package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/joechenrh/golem/internal/router"
)

// ErrQuit signals that the user wants to quit.
var ErrQuit = errors.New("quit")

// handleCommand dispatches an internal or shell colon-command.
func (s *Session) handleCommand(
	ctx context.Context, route router.RouteResult,
) (string, error) {
	switch route.Kind {
	case router.CommandInternal:
		return s.handleInternalCommand(ctx, route.Command, route.Args)
	case router.CommandShell:
		// Shell commands are executed via the shell_exec tool.
		args, _ := json.Marshal(map[string]string{"command": route.Command})
		result, err := s.tools.Execute(ctx, "shell_exec", string(args))
		if err != nil {
			return "Error: " + err.Error(), nil
		}
		return result, nil
	}
	return "", nil
}

// handleInternalCommand processes built-in colon-commands.
func (s *Session) handleInternalCommand(
	_ context.Context, cmd, args string,
) (string, error) {
	switch cmd {
	case "help":
		return s.helpText(), nil

	case "quit":
		return "", ErrQuit

	case "tape.info":
		info := s.tape.Info()
		return fmt.Sprintf("Tape: %s\nEntries: %d | Anchors: %d | Since last anchor: %d",
			info.FilePath, info.TotalEntries, info.AnchorCount, info.EntriesSinceAnchor), nil

	case "tape.search":
		if args == "" {
			return "Usage: :tape.search <query>", nil
		}
		results, err := s.tape.Search(args)
		if err != nil {
			return "Error: " + err.Error(), nil
		}
		if len(results) == 0 {
			return "No matches found.", nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Found %d matches:\n", len(results))
		for _, e := range results {
			fmt.Fprintf(&b, "  [%s] %s: %s\n", e.Kind, e.Timestamp.Format(time.RFC3339), truncateForLog(string(e.Payload), maxLogTruncateLen))
		}
		return b.String(), nil

	case "tools":
		return s.tools.List(), nil

	case "skills":
		list := s.tools.List()
		// Extract just the skills section.
		if idx := strings.Index(list, "Skills"); idx >= 0 {
			return list[idx:], nil
		}
		return "No skills registered.", nil

	case "model":
		if args == "" {
			return fmt.Sprintf("Current model: %s (provider: %s)", s.config.Model, s.llm.Provider()), nil
		}
		// Model switching would require creating a new client — for now just report.
		return fmt.Sprintf("Model switching is not yet supported. Current: %s", s.config.Model), nil

	case "usage":
		return fmt.Sprintf("Session tokens: prompt=%d completion=%d total=%d\nLast turn:      prompt=%d completion=%d total=%d",
			s.sessionUsage.PromptTokens, s.sessionUsage.CompletionTokens, s.sessionUsage.TotalTokens,
			s.turnUsage.PromptTokens, s.turnUsage.CompletionTokens, s.turnUsage.TotalTokens), nil

	case "metrics":
		if s.MetricsSummary != nil {
			return s.MetricsSummary(), nil
		}
		return "Metrics not available.", nil

	case "reset":
		label := args
		if label == "" {
			label = "manual"
		}
		if err := s.tape.AddAnchor(label); err != nil {
			return "Error: " + err.Error(), nil
		}
		return fmt.Sprintf("Anchor added: %s", label), nil

	default:
		return fmt.Sprintf("Unknown command: %s. Type :help for available commands.", cmd), nil
	}
}

func (s *Session) helpText() string {
	return `Available commands:
  :help              Show this help message
  :quit              Exit golem
  :usage             Show token usage statistics
  :metrics           Show operational metrics
  :tape.info         Show tape statistics
  :tape.search <q>   Search tape history
  :tools             List registered tools
  :skills            List discovered skills
  :model [name]      Show or change current model
  :reset [label]     Add a tape anchor (context boundary)
  :<command>         Execute a shell command (e.g., :ls -la)`
}
