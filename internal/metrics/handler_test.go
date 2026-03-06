package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/joechenrh/golem/internal/hooks"
)

type fakeSessionCounter int

func (f fakeSessionCounter) Len() int { return int(f) }

func TestHandler_PrometheusFormat(t *testing.T) {
	collector := NewCollector()

	hook := hooks.NewMetricsHook()
	// Simulate some LLM calls.
	hook.Handle(context.Background(), hooks.Event{Type: hooks.EventBeforeLLMCall})
	hook.Handle(context.Background(), hooks.Event{
		Type: hooks.EventAfterLLMCall,
		Payload: map[string]any{
			"prompt_tokens":     100,
			"completion_tokens": 50,
		},
	})
	hook.Handle(context.Background(), hooks.Event{Type: hooks.EventBeforeLLMCall})
	hook.Handle(context.Background(), hooks.Event{
		Type: hooks.EventAfterLLMCall,
		Payload: map[string]any{
			"prompt_tokens":     200,
			"completion_tokens": 75,
		},
	})
	// Simulate a tool call.
	hook.Handle(context.Background(), hooks.Event{
		Type: hooks.EventAfterToolExec,
		Payload: map[string]any{
			"tool_name": "shell_exec",
			"result":    "ok",
		},
	})
	// Simulate a tool error.
	hook.Handle(context.Background(), hooks.Event{
		Type: hooks.EventAfterToolExec,
		Payload: map[string]any{
			"tool_name": "read_file",
			"result":    "Error: file not found",
		},
	})

	collector.RegisterAgent("test-agent", hook)
	collector.RegisterSessions("test-agent", fakeSessionCounter(5))

	handler := NewHandler(collector)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/debug/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("expected text/plain content type, got %q", ct)
	}

	body := rec.Body.String()

	checks := []string{
		`golem_llm_calls_total{agent="test-agent"} 2`,
		`golem_llm_prompt_tokens_total{agent="test-agent"} 300`,
		`golem_llm_completion_tokens_total{agent="test-agent"} 125`,
		`golem_tool_calls_total{agent="test-agent",tool="shell_exec"} 1`,
		`golem_tool_calls_total{agent="test-agent",tool="read_file"} 1`,
		`golem_tool_errors_total{agent="test-agent",tool="read_file"} 1`,
		`golem_active_sessions{agent="test-agent"} 5`,
		`golem_uptime_seconds`,
		`# TYPE golem_llm_calls_total counter`,
		`# HELP golem_active_sessions`,
	}

	for _, check := range checks {
		if !strings.Contains(body, check) {
			t.Errorf("expected metrics output to contain %q\n\nGot:\n%s", check, body)
		}
	}
}

func TestHandler_EmptyCollector(t *testing.T) {
	collector := NewCollector()
	handler := NewHandler(collector)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/debug/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "golem_uptime_seconds") {
		t.Error("expected uptime metric even with no agents")
	}
}
