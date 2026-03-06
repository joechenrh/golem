package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// NewHandler returns an http.Handler that serves Prometheus-compatible metrics.
func NewHandler(c *Collector) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agents, uptime := c.Snapshot()
		sort.Slice(agents, func(i, j int) bool { return agents[i].Name < agents[j].Name })

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

		var b strings.Builder

		writeHelp(&b, "golem_uptime_seconds", "Process uptime in seconds", "gauge")
		fmt.Fprintf(&b, "golem_uptime_seconds %.1f\n", uptime.Seconds())

		writeHelp(&b, "golem_llm_calls_total", "Total LLM API calls", "counter")
		for _, a := range agents {
			fmt.Fprintf(&b, "golem_llm_calls_total{agent=%q} %d\n", a.Name, a.Snapshot.LLMCalls)
		}

		writeHelp(&b, "golem_llm_errors_total", "LLM API errors", "counter")
		for _, a := range agents {
			fmt.Fprintf(&b, "golem_llm_errors_total{agent=%q} %d\n", a.Name, a.Snapshot.LLMErrors)
		}

		writeHelp(&b, "golem_llm_prompt_tokens_total", "Cumulative prompt tokens", "counter")
		for _, a := range agents {
			fmt.Fprintf(&b, "golem_llm_prompt_tokens_total{agent=%q} %d\n", a.Name, a.Snapshot.TotalPromptTok)
		}

		writeHelp(&b, "golem_llm_completion_tokens_total", "Cumulative completion tokens", "counter")
		for _, a := range agents {
			fmt.Fprintf(&b, "golem_llm_completion_tokens_total{agent=%q} %d\n", a.Name, a.Snapshot.TotalCompleteTok)
		}

		writeHelp(&b, "golem_llm_latency_avg_ms", "Average LLM call latency (recent window)", "gauge")
		for _, a := range agents {
			if len(a.Snapshot.LLMLatencyMs) > 0 {
				var sum int64
				for _, ms := range a.Snapshot.LLMLatencyMs {
					sum += ms
				}
				avg := sum / int64(len(a.Snapshot.LLMLatencyMs))
				fmt.Fprintf(&b, "golem_llm_latency_avg_ms{agent=%q} %d\n", a.Name, avg)
			}
		}

		writeHelp(&b, "golem_tool_calls_total", "Tool invocation count", "counter")
		for _, a := range agents {
			toolNames := sortedKeys(a.Snapshot.ToolCalls)
			for _, tool := range toolNames {
				fmt.Fprintf(&b, "golem_tool_calls_total{agent=%q,tool=%q} %d\n",
					a.Name, tool, a.Snapshot.ToolCalls[tool])
			}
		}

		writeHelp(&b, "golem_tool_errors_total", "Tool error count", "counter")
		for _, a := range agents {
			toolNames := sortedKeys(a.Snapshot.ToolErrors)
			for _, tool := range toolNames {
				fmt.Fprintf(&b, "golem_tool_errors_total{agent=%q,tool=%q} %d\n",
					a.Name, tool, a.Snapshot.ToolErrors[tool])
			}
		}

		writeHelp(&b, "golem_active_sessions", "Currently active sessions", "gauge")
		for _, a := range agents {
			fmt.Fprintf(&b, "golem_active_sessions{agent=%q} %d\n", a.Name, a.ActiveSessions)
		}

		w.Write([]byte(b.String()))
	})
}

func writeHelp(b *strings.Builder, name, help, metricType string) {
	fmt.Fprintf(b, "# HELP %s %s\n", name, help)
	fmt.Fprintf(b, "# TYPE %s %s\n", name, metricType)
}

func sortedKeys(m map[string]int64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
