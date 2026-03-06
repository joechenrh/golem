# Plan: Observability Dashboard (5.9)

## Goal

Expose an HTTP endpoint (`/debug/metrics`) that serves operational metrics
in Prometheus text exposition format. This gives operators visibility into
LLM latency, token consumption, tool call patterns, error rates, and
active sessions without requiring external log aggregation.

## Architecture

```
┌──────────────┐   collects    ┌──────────────────┐
│ MetricsHook  │ ──────────►  │ MetricsCollector  │
│ (per-agent)  │               │ (singleton)       │
└──────────────┘               └───────┬──────────┘
                                       │ Snapshot()
                               ┌───────▼──────────┐
                               │  /debug/metrics   │  ◄── Prometheus scrape
                               │  HTTP handler     │
                               └──────────────────┘
```

## Components

### 1. `internal/metrics/collector.go` — MetricsCollector

A thread-safe singleton that aggregates metrics from one or more
MetricsHook instances (one per agent).

**Exported metrics** (Prometheus naming):

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `golem_llm_calls_total` | counter | `agent` | Total LLM API calls |
| `golem_llm_errors_total` | counter | `agent` | LLM API errors |
| `golem_llm_prompt_tokens_total` | counter | `agent` | Cumulative prompt tokens |
| `golem_llm_completion_tokens_total` | counter | `agent` | Cumulative completion tokens |
| `golem_llm_latency_ms` | summary | `agent` | LLM call latency (avg/min/max from ring buffer) |
| `golem_tool_calls_total` | counter | `agent`, `tool` | Tool invocation count |
| `golem_tool_errors_total` | counter | `agent`, `tool` | Tool error count |
| `golem_active_sessions` | gauge | `agent` | Currently active sessions |
| `golem_uptime_seconds` | gauge | | Process uptime |

### 2. `internal/metrics/handler.go` — HTTP handler

- Registers at `/debug/metrics`
- Returns `text/plain; version=0.0.4` content type
- Writes metrics in Prometheus text exposition format
- No external dependency (hand-roll the simple text format)

### 3. Wiring in `internal/app/app.go` and `cmd/golem/main.go`

- Create a `MetricsCollector` in main
- Pass it to each agent's MetricsHook
- Start the HTTP server on a configurable port (`GOLEM_METRICS_PORT`, default 9090)
- Only start the server when the port is configured (opt-in)

## Implementation Steps

1. Create `internal/metrics/collector.go` with `MetricsCollector`
   - `RegisterAgent(name string, hook *hooks.MetricsHook)`
   - `RegisterSessionManager(name string, sm *agent.SessionManager)`
   - `Snapshot() []MetricLine` — snapshot all current values

2. Create `internal/metrics/handler.go` with the HTTP handler
   - `NewHandler(collector *MetricsCollector) http.Handler`
   - Prometheus text format serialization

3. Add `MetricsPort` config field to `internal/config/config.go`
   - `GOLEM_METRICS_PORT` env var, default `""` (disabled)

4. Wire in `cmd/golem/main.go`
   - If metrics port configured, start `http.ListenAndServe`
   - Register each agent's MetricsHook with the collector

5. Add tests
   - Unit test for Prometheus text serialization
   - Integration test: start server, scrape, parse metrics

## Non-goals (out of scope)

- Grafana dashboards (users can build their own)
- Push-based metrics (only pull/scrape)
- Distributed tracing / OpenTelemetry (future work)
- Authentication on the metrics endpoint (local-only by default)

## Config

```env
GOLEM_METRICS_PORT=9090   # set to enable metrics server
```

## Example output

```
# HELP golem_llm_calls_total Total LLM API calls
# TYPE golem_llm_calls_total counter
golem_llm_calls_total{agent="default"} 42
# HELP golem_llm_errors_total LLM API errors
# TYPE golem_llm_errors_total counter
golem_llm_errors_total{agent="default"} 1
# HELP golem_llm_prompt_tokens_total Cumulative prompt tokens
# TYPE golem_llm_prompt_tokens_total counter
golem_llm_prompt_tokens_total{agent="default"} 128000
# HELP golem_llm_completion_tokens_total Cumulative completion tokens
# TYPE golem_llm_completion_tokens_total counter
golem_llm_completion_tokens_total{agent="default"} 32000
# HELP golem_tool_calls_total Tool invocation count
# TYPE golem_tool_calls_total counter
golem_tool_calls_total{agent="default",tool="shell_exec"} 15
golem_tool_calls_total{agent="default",tool="read_file"} 8
# HELP golem_active_sessions Currently active sessions
# TYPE golem_active_sessions gauge
golem_active_sessions{agent="default"} 3
# HELP golem_uptime_seconds Process uptime
# TYPE golem_uptime_seconds gauge
golem_uptime_seconds 3600.5
```
