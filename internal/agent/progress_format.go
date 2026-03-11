package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/joechenrh/golem/internal/hooks"
)

// FormatProgress formats a StatusSnapshot into a human-readable progress
// report for the /status slash command.
func FormatProgress(snap StatusSnapshot) string {
	var b strings.Builder

	fmt.Fprintf(&b, "📊 Progress — Iteration %d/%d\n",
		snap.State.Iteration, snap.State.MaxIter)

	if snap.State.Phase != "" {
		fmt.Fprintf(&b, "Phase: %q\n", snap.State.Phase)
	}

	if snap.State.ActiveTool != "" {
		elapsed := time.Since(snap.State.ToolStarted).Truncate(time.Second)
		fmt.Fprintf(&b, "Current: %s running (%s elapsed)\n",
			snap.State.ActiveTool, elapsed)
	}

	if len(snap.RecentEvents) > 0 {
		hasToolEvents := false
		for _, e := range snap.RecentEvents {
			if e.Type == hooks.EventAfterToolExec || e.Type == hooks.EventBeforeToolExec {
				hasToolEvents = true
				break
			}
		}
		if hasToolEvents {
			b.WriteString("\nRecent activity:\n")
			for _, e := range snap.RecentEvents {
				switch e.Type {
				case hooks.EventAfterToolExec:
					name := payloadStr(e.Payload, "tool_name")
					durationMs := payloadInt(e.Payload, "duration_ms")
					errStr := payloadStr(e.Payload, "error")
					if errStr != "" {
						fmt.Fprintf(&b, "  ✗ %s (%dms, error)\n", name, durationMs)
					} else {
						fmt.Fprintf(&b, "  ✓ %s (%dms)\n", name, durationMs)
					}
				case hooks.EventBeforeToolExec:
					name := payloadStr(e.Payload, "tool_name")
					fmt.Fprintf(&b, "  ⟳ %s (running...)\n", name)
				}
			}
		}
	}

	if snap.State.RunningTasks > 0 {
		fmt.Fprintf(&b, "\nBackground tasks: %d running\n", snap.State.RunningTasks)
	}

	return b.String()
}
