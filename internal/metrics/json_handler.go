package metrics

import (
	"cmp"
	"encoding/json"
	"net/http"
	"slices"
)

type jsonSnapshot struct {
	UptimeSeconds float64         `json:"uptime_seconds"`
	Agents        []jsonAgentData `json:"agents"`
}

type jsonAgentData struct {
	Name             string         `json:"name"`
	LLMCalls         int64          `json:"llm_calls"`
	LLMErrors        int64          `json:"llm_errors"`
	PromptTokens     int64          `json:"prompt_tokens"`
	CompletionTokens int64          `json:"completion_tokens"`
	LatencyAvgMs     *int64         `json:"latency_avg_ms"`
	ActiveSessions   int            `json:"active_sessions"`
	Tools            []jsonToolData `json:"tools,omitempty"`
}

type jsonToolData struct {
	Name   string `json:"name"`
	Calls  int64  `json:"calls"`
	Errors int64  `json:"errors"`
}

// NewJSONHandler returns an http.Handler that serves metrics as JSON.
func NewJSONHandler(c *Collector) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agents, uptime := c.Snapshot()
		slices.SortFunc(agents, func(a, b AgentMetrics) int { return cmp.Compare(a.Name, b.Name) })

		snap := jsonSnapshot{
			UptimeSeconds: uptime.Seconds(),
			Agents:        make([]jsonAgentData, len(agents)),
		}

		for i, a := range agents {
			ad := jsonAgentData{
				Name:             a.Name,
				LLMCalls:         a.Snapshot.LLMCalls,
				LLMErrors:        a.Snapshot.LLMErrors,
				PromptTokens:     a.Snapshot.TotalPromptTok,
				CompletionTokens: a.Snapshot.TotalCompleteTok,
				ActiveSessions:   a.ActiveSessions,
			}

			if len(a.Snapshot.LLMLatencyMs) > 0 {
				var sum int64
				for _, ms := range a.Snapshot.LLMLatencyMs {
					sum += ms
				}
				avg := sum / int64(len(a.Snapshot.LLMLatencyMs))
				ad.LatencyAvgMs = &avg
			}

			toolNames := sortedKeys(a.Snapshot.ToolCalls)
			for _, name := range toolNames {
				ad.Tools = append(ad.Tools, jsonToolData{
					Name:   name,
					Calls:  a.Snapshot.ToolCalls[name],
					Errors: a.Snapshot.ToolErrors[name],
				})
			}

			snap.Agents[i] = ad
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(snap)
	})
}
