package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/joechenrh/golem/internal/hooks"
)

// mockChannel records SendDirect calls for testing.
type mockChannel struct {
	mu      sync.Mutex
	sent    []string
	sendErr error
}

func (m *mockChannel) SendDirect(_ context.Context, _, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, text)
	return m.sendErr
}

func (m *mockChannel) sentMessages() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.sent...)
}

func TestProgressReporter_SendsPhaseUpdate(t *testing.T) {
	ch := &mockChannel{}
	r := NewProgressReporter(ch, "chat-1", 0, zap.NewNop())

	r.Handle(context.Background(), hooks.Event{
		Type:    hooks.EventPhaseUpdate,
		Payload: map[string]any{"summary": "analyzing codebase"},
	})

	msgs := ch.sentMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0] != "📋 analyzing codebase" {
		t.Errorf("unexpected message: %q", msgs[0])
	}
}

func TestProgressReporter_IgnoresNonPhaseEvents(t *testing.T) {
	ch := &mockChannel{}
	r := NewProgressReporter(ch, "chat-1", 0, zap.NewNop())

	r.Handle(context.Background(), hooks.Event{
		Type:    hooks.EventIterationStart,
		Payload: map[string]any{"iteration": 1},
	})

	msgs := ch.sentMessages()
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages for non-phase event, got %d", len(msgs))
	}
}

func TestProgressReporter_Throttle(t *testing.T) {
	ch := &mockChannel{}
	r := NewProgressReporter(ch, "chat-1", 1*time.Hour, zap.NewNop())

	r.Handle(context.Background(), hooks.Event{
		Type:    hooks.EventPhaseUpdate,
		Payload: map[string]any{"summary": "first"},
	})
	r.Handle(context.Background(), hooks.Event{
		Type:    hooks.EventPhaseUpdate,
		Payload: map[string]any{"summary": "second (throttled)"},
	})

	msgs := ch.sentMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message (second throttled), got %d", len(msgs))
	}
	if msgs[0] != "📋 first" {
		t.Errorf("unexpected message: %q", msgs[0])
	}
}
