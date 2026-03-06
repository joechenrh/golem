# Design 13 — Observability / Metrics

## 1. Overview

Golem exposes a Prometheus-compatible metrics endpoint at `/debug/metrics`.
The system collects per-agent operational data (LLM usage, token counts, tool
invocations, latency) and renders it in the Prometheus text exposition format
so that any standard scraper can ingest it without additional adapters.

The implementation spans three files. `internal/hooks/metrics.go` contains the
`MetricsHook`, which performs event-driven data collection.
`internal/metrics/collector.go` defines the `Collector`, which aggregates hooks
across agents. `internal/metrics/handler.go` provides the HTTP handler that
renders metrics in Prometheus text format.

## 2. MetricsHook

The MetricsHook is covered in detail in `08-hooks.md`. In brief, it uses atomic
counters for LLM call and token tracking and mutex-guarded maps for per-tool
counters. A ring buffer of the last 100 LLM call latencies provides a
recent-window average.

`Snapshot()` acquires the mutex, deep-copies the tool maps and the latency
slice, and reads the atomic counters. The resulting `MetricsSnapshot` struct is
safe for concurrent use by the collector.

## 3. Collector

The `Collector` aggregates metrics sources from multiple agents behind a single
mutex. It holds a map of agent sources keyed by agent name and records the
process start time for uptime calculation.

Two registration methods populate the agent map. `RegisterAgent(name, *MetricsHook)`
associates the hook that collects LLM and tool counters.
`RegisterSessions(name, SessionCounter)` associates an object whose `Len() int`
method reports the active session count (typically `SessionManager`). Both
methods create-or-update the entry, so they can be called in any order.

`Snapshot()` iterates over all registered agents, calls each hook's
`Snapshot()`, reads the session count, and returns `([]AgentMetrics, time.Duration)`
where the duration is process uptime.

## 4. Handler

`NewHandler(c *Collector)` returns an `http.Handler` that renders all metrics
in Prometheus text exposition format (`Content-Type: text/plain; version=0.0.4`).
Each metric is preceded by `# HELP` and `# TYPE` comment lines. Agents are
sorted alphabetically for deterministic output. Tool-level metrics sort tool
names alphabetically within each agent.

### Metric reference

| Metric name | Type | Labels | Description |
|-------------|------|--------|-------------|
| `golem_uptime_seconds` | gauge | _(none)_ | Process uptime |
| `golem_llm_calls_total` | counter | `agent` | Total LLM API calls |
| `golem_llm_errors_total` | counter | `agent` | LLM API errors |
| `golem_llm_prompt_tokens_total` | counter | `agent` | Cumulative prompt tokens |
| `golem_llm_completion_tokens_total` | counter | `agent` | Cumulative completion tokens |
| `golem_llm_latency_avg_ms` | gauge | `agent` | Average latency over last 100 calls |
| `golem_tool_calls_total` | counter | `agent`, `tool` | Per-tool invocation count |
| `golem_tool_errors_total` | counter | `agent`, `tool` | Per-tool error count |
| `golem_active_sessions` | gauge | `agent` | Currently active chat sessions |

`golem_llm_latency_avg_ms` is only emitted when the latency buffer is
non-empty. The average is computed on each scrape from the raw ring buffer
values, not stored incrementally.

## 5. Wiring

During startup, `BuildAgent` creates a `MetricsHook`, registers it on the hook bus, and exposes it as `AgentInstance.MetricsHook`. If `cfg.MetricsPort` is non-empty, `main()` creates a `Collector`, registers the agent's hook and session manager, mounts the handler at `/debug/metrics`, and starts the HTTP server in a goroutine.

```
GOLEM_METRICS_PORT=9090  ->  http://localhost:9090/debug/metrics
GOLEM_METRICS_PORT=""    ->  metrics server disabled (default)
```

The config field is `MetricsPort string` in `internal/config/config.go`, read
from the `GOLEM_METRICS_PORT` environment variable with a default of `""`
(disabled).

## 6. Current Gaps

- **No histogram/percentile latency.** The handler computes a simple average
  over the ring buffer. Prometheus conventions prefer histograms or summaries
  for latency so that p50/p95/p99 can be derived.
- **Ring buffer is a slice shift.** The oldest entry is dropped by reslicing,
  which never reclaims the underlying array. Over long-running processes this is
  negligible (100 int64s) but a true ring buffer with a fixed array and index
  pointer would be cleaner.
- **No per-session metrics.** Only the aggregate session count is tracked;
  there is no per-session breakdown of LLM calls or token spend.
- **No request-scoped latency for tools.** Tool execution time is not
  recorded, only success/failure.
- **Single-process only.** The collector is in-memory with no federation or
  push-gateway support for multi-instance deployments.
- **No graceful shutdown.** The metrics HTTP server runs in a bare goroutine
  with no `http.Server.Shutdown` call, so in-flight scrapes may be dropped on
  SIGTERM.
- **`golem_uptime_seconds` lacks labels.** It is a global metric, not
  per-agent, which is correct but means it cannot distinguish restart times in
  a multi-agent future if agents have different lifecycles.
