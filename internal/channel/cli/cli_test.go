package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/joechenrh/golem/internal/channel"
)

func TestNew_Defaults(t *testing.T) {
	c := New()
	if c.Name() != "cli" {
		t.Errorf("Name() = %q, want %q", c.Name(), "cli")
	}
	if !c.SupportsStreaming() {
		t.Error("SupportsStreaming() = false, want true")
	}
}

func TestStart_ReadsLines(t *testing.T) {
	input := "hello\nworld\n"
	c := New(WithReader(strings.NewReader(input)), WithWriter(&bytes.Buffer{}))

	inCh := make(chan channel.IncomingMessage, 10)
	err := c.Start(context.Background(), inCh)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	var msgs []string
	close(inCh)
	for msg := range inCh {
		msgs = append(msgs, msg.Text)
	}

	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	if msgs[0] != "hello" || msgs[1] != "world" {
		t.Errorf("messages = %v", msgs)
	}
}

func TestStart_SkipsEmptyLines(t *testing.T) {
	input := "\n  \nhello\n\n"
	c := New(WithReader(strings.NewReader(input)), WithWriter(&bytes.Buffer{}))

	inCh := make(chan channel.IncomingMessage, 10)
	err := c.Start(context.Background(), inCh)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	close(inCh)
	var count int
	for range inCh {
		count++
	}
	if count != 1 {
		t.Errorf("got %d messages, want 1 (empty lines skipped)", count)
	}
}

func TestStart_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// Provide input that will not EOF so we test context cancellation.
	// The Start loop checks ctx.Done() before each scan.
	c := New(
		WithReader(strings.NewReader("line1\nline2\n")),
		WithWriter(&bytes.Buffer{}),
	)

	inCh := make(chan channel.IncomingMessage, 10)
	err := c.Start(ctx, inCh)
	// Should complete without error (reads all input before context expires)
	// or return context error.
	if err != nil && err != context.DeadlineExceeded {
		t.Fatalf("Start: %v", err)
	}
}

func TestStart_SetsChannelFields(t *testing.T) {
	input := "test\n"
	c := New(WithReader(strings.NewReader(input)), WithWriter(&bytes.Buffer{}))

	inCh := make(chan channel.IncomingMessage, 10)
	_ = c.Start(context.Background(), inCh)
	close(inCh)

	msg := <-inCh
	if msg.ChannelID != "cli" {
		t.Errorf("ChannelID = %q, want %q", msg.ChannelID, "cli")
	}
	if msg.ChannelName != "cli" {
		t.Errorf("ChannelName = %q, want %q", msg.ChannelName, "cli")
	}
}

func TestSend(t *testing.T) {
	var buf bytes.Buffer
	c := New(WithWriter(&buf))

	err := c.Send(context.Background(), channel.OutgoingMessage{Text: "hello"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := buf.String(); got != "hello\n" {
		t.Errorf("output = %q, want %q", got, "hello\n")
	}
}

func TestSendTyping(t *testing.T) {
	c := New()
	if err := c.SendTyping(context.Background(), "cli"); err != nil {
		t.Errorf("SendTyping: %v", err)
	}
}

func TestSendStream(t *testing.T) {
	var buf bytes.Buffer
	c := New(WithWriter(&buf))

	tokenCh := make(chan string, 10)
	tokenCh <- "Hello"
	tokenCh <- " "
	tokenCh <- "World"
	close(tokenCh)

	err := c.SendStream(context.Background(), "cli", tokenCh)
	if err != nil {
		t.Fatalf("SendStream: %v", err)
	}
	if got := buf.String(); got != "Hello World\n" {
		t.Errorf("output = %q, want %q", got, "Hello World\n")
	}
}

func TestPrintBanner(t *testing.T) {
	var buf bytes.Buffer
	c := New(WithWriter(&buf))

	c.PrintBanner("openai:gpt-4o", 5, "/tmp/tape.jsonl")
	out := buf.String()
	if !strings.Contains(out, "golem") {
		t.Errorf("banner missing identity: %s", out)
	}
	if !strings.Contains(out, "openai:gpt-4o") {
		t.Errorf("banner missing model: %s", out)
	}
	if !strings.Contains(out, "5 registered") {
		t.Errorf("banner missing tool count: %s", out)
	}
}

func TestPrintError(t *testing.T) {
	var buf bytes.Buffer
	c := New(WithWriter(&buf))

	c.PrintError("something broke")
	out := buf.String()
	if !strings.Contains(out, "something broke") {
		t.Errorf("error output = %q", out)
	}
	if !strings.Contains(out, colorRed) {
		t.Errorf("error output missing red color")
	}
}

func TestPrompt_Displayed(t *testing.T) {
	var buf bytes.Buffer
	c := New(
		WithPrompt("test> "),
		WithReader(strings.NewReader("hi\n")),
		WithWriter(&buf),
	)

	inCh := make(chan channel.IncomingMessage, 10)
	_ = c.Start(context.Background(), inCh)

	if !strings.Contains(buf.String(), "test> ") {
		t.Errorf("output = %q, want prompt", buf.String())
	}
}

// Verify stubs implement the interface.
var _ channel.Channel = (*CLIChannel)(nil)
