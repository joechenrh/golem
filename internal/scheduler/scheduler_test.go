package scheduler

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/joechenrh/golem/internal/channel"
)

// mockChannel implements channel.Channel for testing.
type mockChannel struct {
	mu   sync.Mutex
	sent []channel.OutgoingMessage
}

func (m *mockChannel) Name() string { return "mock" }
func (m *mockChannel) Start(_ context.Context, _ chan<- channel.IncomingMessage) error {
	return nil
}
func (m *mockChannel) Send(_ context.Context, msg channel.OutgoingMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return nil
}
func (m *mockChannel) SendTyping(_ context.Context, _ string) error                  { return nil }
func (m *mockChannel) SupportsStreaming() bool                                       { return false }
func (m *mockChannel) SendStream(_ context.Context, _ string, _ <-chan string) error { return nil }

func (m *mockChannel) messages() []channel.OutgoingMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]channel.OutgoingMessage, len(m.sent))
	copy(result, m.sent)
	return result
}

// mockSessionFactory implements SessionFactory for testing.
type mockSessionFactory struct {
	response string
	err      error
}

func (f *mockSessionFactory) HandleScheduledPrompt(
	_ context.Context, _ string, _ channel.IncomingMessage,
) (string, error) {
	return f.response, f.err
}

func TestScheduler_FiresDueSchedule(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "schedules.json"))

	// Add a schedule that should have already fired (last fired 2 hours ago, @hourly).
	id, err := store.Add("@hourly", "do stuff", "mock", "ch_1", "hourly task")
	if err != nil {
		t.Fatal(err)
	}
	store.UpdateLastFired(id, time.Now().Add(-2*time.Hour))

	ch := &mockChannel{}
	channels := map[string]channel.Channel{"mock": ch}
	factory := &mockSessionFactory{response: "done!"}

	sched := New(store, channels, factory, nil, zap.NewNop())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run a single tick.
	sched.tick(ctx)

	msgs := ch.messages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Text != "done!" {
		t.Errorf("expected %q, got %q", "done!", msgs[0].Text)
	}
	if msgs[0].ChannelID != "ch_1" {
		t.Errorf("expected channel ID %q, got %q", "ch_1", msgs[0].ChannelID)
	}
}

func TestScheduler_SkipsNotDueSchedule(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "schedules.json"))

	id, err := store.Add("@hourly", "do stuff", "mock", "ch_1", "hourly task")
	if err != nil {
		t.Fatal(err)
	}
	// Last fired just now — next fire is in ~1 hour.
	store.UpdateLastFired(id, time.Now())

	ch := &mockChannel{}
	channels := map[string]channel.Channel{"mock": ch}
	factory := &mockSessionFactory{response: "done!"}

	sched := New(store, channels, factory, nil, zap.NewNop())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sched.tick(ctx)

	if len(ch.messages()) != 0 {
		t.Error("expected no messages for not-yet-due schedule")
	}
}

func TestScheduler_HandlesSessionError(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "schedules.json"))

	id, _ := store.Add("@hourly", "do stuff", "mock", "ch_1", "broken task")
	store.UpdateLastFired(id, time.Now().Add(-2*time.Hour))

	ch := &mockChannel{}
	channels := map[string]channel.Channel{"mock": ch}
	factory := &mockSessionFactory{err: fmt.Errorf("LLM exploded")}

	sched := New(store, channels, factory, nil, zap.NewNop())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sched.tick(ctx)

	msgs := ch.messages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 error message, got %d", len(msgs))
	}
	if !strings.Contains(msgs[0].Text, "failed") {
		t.Errorf("expected error notification, got %q", msgs[0].Text)
	}
}

func TestScheduler_MissingChannel(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "schedules.json"))

	id, _ := store.Add("@hourly", "do stuff", "nonexistent", "ch_1", "orphan task")
	store.UpdateLastFired(id, time.Now().Add(-2*time.Hour))

	channels := map[string]channel.Channel{}
	factory := &mockSessionFactory{response: "done!"}

	sched := New(store, channels, factory, nil, zap.NewNop())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Should not panic.
	sched.tick(ctx)
}
