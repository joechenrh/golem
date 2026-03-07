package middleware

import (
	"context"
	"testing"
	"time"

	"github.com/joechenrh/golem/internal/redact"
)

func TestRedactMiddleware(t *testing.T) {
	r := redact.New()
	mw := Redact(r)

	next := func(_ context.Context, args string) (string, error) {
		return "API_KEY=sk-proj-abc123def456ghi789jkl found", nil
	}

	got, err := mw(context.Background(), "shell_exec", "{}", next)
	if err != nil {
		t.Fatal(err)
	}
	want := "API_KEY=[REDACTED:env_secret] found"
	if got != want {
		t.Errorf("middleware: got %q, want %q", got, want)
	}
}

func TestRedactMiddleware_Error(t *testing.T) {
	r := redact.New()
	mw := Redact(r)

	wantErr := context.DeadlineExceeded
	next := func(_ context.Context, args string) (string, error) {
		return "API_KEY=secret123", wantErr
	}

	got, err := mw(context.Background(), "shell_exec", "{}", next)
	if err != wantErr {
		t.Fatalf("expected error %v, got %v", wantErr, err)
	}
	// On error, result is returned as-is (not redacted).
	if got != "API_KEY=secret123" {
		t.Errorf("on error, result should not be redacted: got %q", got)
	}
}

func TestCacheMiddleware_HitAndMiss(t *testing.T) {
	calls := 0
	next := func(_ context.Context, args string) (string, error) {
		calls++
		return "result:" + args, nil
	}

	cm := NewCacheMiddleware(time.Minute, []string{"read_file"}, nil)
	mw := cm.Middleware()

	// First call — cache miss.
	got, err := mw(context.Background(), "read_file", `{"path":"a.txt"}`, next)
	if err != nil {
		t.Fatal(err)
	}
	if got != `result:{"path":"a.txt"}` {
		t.Errorf("got %q", got)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}

	// Second call — cache hit.
	got, _ = mw(context.Background(), "read_file", `{"path":"a.txt"}`, next)
	if got != `result:{"path":"a.txt"}` {
		t.Errorf("got %q on cache hit", got)
	}
	if calls != 1 {
		t.Errorf("calls = %d after cache hit, want 1", calls)
	}
}

func TestCacheMiddleware_NonCacheableTool(t *testing.T) {
	calls := 0
	next := func(_ context.Context, args string) (string, error) {
		calls++
		return "ok", nil
	}

	cm := NewCacheMiddleware(time.Minute, []string{"read_file"}, nil)
	mw := cm.Middleware()

	// shell_exec is not cacheable — should always call through.
	mw(context.Background(), "shell_exec", "{}", next)
	mw(context.Background(), "shell_exec", "{}", next)
	if calls != 2 {
		t.Errorf("calls = %d, want 2 (not cacheable)", calls)
	}
}

func TestCacheMiddleware_InvalidatorTool(t *testing.T) {
	calls := 0
	next := func(_ context.Context, args string) (string, error) {
		calls++
		return "v" + string(rune('0'+calls)), nil
	}

	cm := NewCacheMiddleware(time.Minute, []string{"read_file"}, []string{"write_file"})
	mw := cm.Middleware()

	// Populate cache.
	mw(context.Background(), "read_file", `{"path":"a.txt"}`, next)
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}

	// write_file should invalidate the cache.
	mw(context.Background(), "write_file", `{"path":"a.txt","content":"new"}`, next)
	if calls != 2 {
		t.Fatalf("calls after write = %d, want 2", calls)
	}

	// read_file should miss the cache now.
	got, _ := mw(context.Background(), "read_file", `{"path":"a.txt"}`, next)
	if calls != 3 {
		t.Errorf("calls after invalidated read = %d, want 3", calls)
	}
	if got != "v3" {
		t.Errorf("got %q, want %q", got, "v3")
	}
}

func TestCacheMiddleware_Invalidate(t *testing.T) {
	calls := 0
	next := func(_ context.Context, args string) (string, error) {
		calls++
		return "v" + string(rune('0'+calls)), nil
	}

	cm := NewCacheMiddleware(time.Minute, []string{"read_file"}, nil)
	mw := cm.Middleware()

	mw(context.Background(), "read_file", `{"path":"a.txt"}`, next)
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}

	cm.Invalidate()

	got, _ := mw(context.Background(), "read_file", `{"path":"a.txt"}`, next)
	if calls != 2 {
		t.Errorf("calls after invalidate = %d, want 2", calls)
	}
	if got != "v2" {
		t.Errorf("got %q after invalidate, want %q", got, "v2")
	}
}
