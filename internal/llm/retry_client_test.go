package llm

import (
	"context"
	"fmt"
	"testing"
	"time"
)

type failStreamClient struct {
	attempts int
	failN    int // fail the first N attempts
}

func (c *failStreamClient) Provider() Provider { return ProviderOpenAI }

func (c *failStreamClient) Chat(_ context.Context, _ ChatRequest) (*ChatResponse, error) {
	return &ChatResponse{Content: "ok"}, nil
}

func (c *failStreamClient) ChatStream(_ context.Context, _ ChatRequest) (<-chan StreamEvent, error) {
	c.attempts++
	if c.attempts <= c.failN {
		return nil, &APIError{StatusCode: 429, Message: "rate limited", Retryable: true}
	}
	ch := make(chan StreamEvent, 2)
	ch <- StreamEvent{Type: StreamContentDelta, Content: "hello"}
	ch <- StreamEvent{Type: StreamDone}
	close(ch)
	return ch, nil
}

func TestRetryClient_ChatStream_RetriesOnRetryableError(t *testing.T) {
	inner := &failStreamClient{failN: 2}
	client := NewRetryClient(inner, 3, 1*time.Millisecond, nil)

	ch, err := client.ChatStream(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var content string
	for ev := range ch {
		if ev.Type == StreamContentDelta {
			content += ev.Content
		}
	}
	if content != "hello" {
		t.Errorf("content = %q, want %q", content, "hello")
	}
	if inner.attempts != 3 {
		t.Errorf("attempts = %d, want 3", inner.attempts)
	}
}

func TestRetryClient_ChatStream_ExhaustsRetries(t *testing.T) {
	inner := &failStreamClient{failN: 5}
	client := NewRetryClient(inner, 3, 1*time.Millisecond, nil)

	_, err := client.ChatStream(context.Background(), ChatRequest{})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if inner.attempts != 3 {
		t.Errorf("attempts = %d, want 3", inner.attempts)
	}
}

func TestRetryClient_ChatStream_NoRetryOnNonRetryable(t *testing.T) {
	inner := &nonRetryableClient{}
	client := NewRetryClient(inner, 3, 1*time.Millisecond, nil)

	_, err := client.ChatStream(context.Background(), ChatRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	if inner.attempts != 1 {
		t.Errorf("attempts = %d, want 1 (no retry for 4xx)", inner.attempts)
	}
}

type nonRetryableClient struct {
	attempts int
}

func (c *nonRetryableClient) Provider() Provider { return ProviderOpenAI }
func (c *nonRetryableClient) Chat(_ context.Context, _ ChatRequest) (*ChatResponse, error) {
	return nil, fmt.Errorf("not implemented")
}
func (c *nonRetryableClient) ChatStream(_ context.Context, _ ChatRequest) (<-chan StreamEvent, error) {
	c.attempts++
	return nil, &APIError{StatusCode: 400, Message: "bad request", Retryable: false}
}

func TestRetryClient_ChatStream_RespectsContextCancellation(t *testing.T) {
	inner := &failStreamClient{failN: 5}
	client := NewRetryClient(inner, 5, 50*time.Millisecond, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := client.ChatStream(ctx, ChatRequest{})
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}
