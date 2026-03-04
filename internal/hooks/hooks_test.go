package hooks

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// testHook records events for assertions.
type testHook struct {
	name   string
	events []Event
	err    error // if set, Handle returns this error
}

func (h *testHook) Name() string { return h.name }

func (h *testHook) Handle(_ context.Context, event Event) error {
	h.events = append(h.events, event)
	return h.err
}

func TestBus_EmptyBus(t *testing.T) {
	bus := NewBus()
	err := bus.Emit(context.Background(), Event{Type: EventUserMessage})
	if err != nil {
		t.Fatalf("empty bus should not error, got: %v", err)
	}
}

func TestBus_DispatchesToHooks(t *testing.T) {
	bus := NewBus()
	h1 := &testHook{name: "h1"}
	h2 := &testHook{name: "h2"}
	bus.Register(h1)
	bus.Register(h2)

	event := Event{
		Type:    EventUserMessage,
		Payload: map[string]interface{}{"text": "hello"},
	}
	err := bus.Emit(context.Background(), event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(h1.events) != 1 {
		t.Errorf("h1 got %d events, want 1", len(h1.events))
	}
	if len(h2.events) != 1 {
		t.Errorf("h2 got %d events, want 1", len(h2.events))
	}
}

func TestBus_BeforeEventErrorStopsChain(t *testing.T) {
	bus := NewBus()
	blocker := &testHook{name: "blocker", err: errors.New("blocked")}
	second := &testHook{name: "second"}
	bus.Register(blocker)
	bus.Register(second)

	err := bus.Emit(context.Background(), Event{Type: EventBeforeToolExec})
	if err == nil {
		t.Fatal("expected error from before_tool_exec")
	}
	if err.Error() != "blocked" {
		t.Errorf("error = %q, want %q", err.Error(), "blocked")
	}
	if len(second.events) != 0 {
		t.Error("second hook should not have been called after blocker error")
	}
}

func TestBus_AfterEventErrorDoesNotBlock(t *testing.T) {
	bus := NewBus()
	failing := &testHook{name: "failing", err: errors.New("oops")}
	second := &testHook{name: "second"}
	bus.Register(failing)
	bus.Register(second)

	err := bus.Emit(context.Background(), Event{Type: EventAfterToolExec})
	if err != nil {
		t.Fatalf("after_* errors should not propagate, got: %v", err)
	}
	if len(second.events) != 1 {
		t.Error("second hook should still be called after after_* error")
	}
}

func TestLoggingHook_AllEventTypes(t *testing.T) {
	logger := zaptest.NewLogger(t)
	h := NewLoggingHook(logger)

	if h.Name() != "logging" {
		t.Errorf("name = %q, want %q", h.Name(), "logging")
	}

	events := []EventType{
		EventUserMessage,
		EventBeforeLLMCall,
		EventAfterLLMCall,
		EventBeforeToolExec,
		EventAfterToolExec,
		EventError,
	}

	for _, et := range events {
		err := h.Handle(context.Background(), Event{
			Type:    et,
			Payload: map[string]interface{}{"key": "value"},
		})
		if err != nil {
			t.Errorf("Handle(%s) returned error: %v", et, err)
		}
	}
}

func TestLoggingHook_NilPayload(t *testing.T) {
	logger := zap.NewNop()
	h := NewLoggingHook(logger)

	err := h.Handle(context.Background(), Event{Type: EventUserMessage})
	if err != nil {
		t.Fatalf("nil payload should not error: %v", err)
	}
}
