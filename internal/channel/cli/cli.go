package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/joechenrh/golem/internal/channel"
)

// ANSI color codes.
const (
	colorReset = "\033[0m"
	colorRed   = "\033[31m"
	colorGreen = "\033[32m"
	colorDim   = "\033[2m"
	colorBold  = "\033[1m"
)

// CLIChannel implements channel.Channel for an interactive terminal REPL.
type CLIChannel struct {
	prompt          string
	reader          io.Reader
	writer          io.Writer
	thinkingCleared bool // set by SendStream/Send when they clear the indicator
}

// Option configures a CLIChannel.
type Option func(*CLIChannel)

// WithPrompt sets a custom prompt string.
func WithPrompt(p string) Option {
	return func(c *CLIChannel) { c.prompt = p }
}

// WithReader sets a custom input reader (useful for testing).
func WithReader(r io.Reader) Option {
	return func(c *CLIChannel) { c.reader = r }
}

// WithWriter sets a custom output writer (useful for testing).
func WithWriter(w io.Writer) Option {
	return func(c *CLIChannel) { c.writer = w }
}

// New creates a CLIChannel with the given options.
func New(opts ...Option) *CLIChannel {
	c := &CLIChannel{
		prompt: "golem> ",
		reader: os.Stdin,
		writer: os.Stdout,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *CLIChannel) Name() string { return "cli" }

// Start runs the REPL loop, sending each line to inCh. Blocks until context
// is cancelled or EOF is reached.
func (c *CLIChannel) Start(ctx context.Context, inCh chan<- channel.IncomingMessage) error {
	scanner := bufio.NewScanner(c.reader)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		fmt.Fprint(c.writer, c.prompt)
		if !scanner.Scan() {
			// EOF or read error.
			break
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		done := make(chan struct{})
		msg := channel.IncomingMessage{
			ChannelID:   "cli",
			ChannelName: "cli",
			Text:        line,
			Done:        done,
		}

		select {
		case inCh <- msg:
		case <-ctx.Done():
			return ctx.Err()
		}

		// Show thinking indicator and wait for processing to complete.
		c.thinkingCleared = false
		fmt.Fprintf(c.writer, "%sThinking...%s", colorDim, colorReset)
		select {
		case <-done:
		case <-ctx.Done():
			fmt.Fprint(c.writer, "\r\033[K")
			return ctx.Err()
		}
		if !c.thinkingCleared {
			fmt.Fprint(c.writer, "\r\033[K")
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}
	return nil
}

// Send prints a message to the terminal, clearing any pending indicator first.
func (c *CLIChannel) Send(_ context.Context, msg channel.OutgoingMessage) error {
	fmt.Fprintf(c.writer, "\r\033[K%s\n", msg.Text)
	c.thinkingCleared = true
	return nil
}

// SendTyping is a no-op for CLI.
func (c *CLIChannel) SendTyping(_ context.Context, _ string) error {
	return nil
}

// SupportsStreaming returns true; the CLI prints tokens as they arrive.
func (c *CLIChannel) SupportsStreaming() bool { return true }

// SendStream reads tokens from tokenCh and prints them incrementally.
func (c *CLIChannel) SendStream(_ context.Context, _ string, tokenCh <-chan string) error {
	first := true
	for tok := range tokenCh {
		if first {
			// Clear the "Thinking..." indicator before first output.
			fmt.Fprint(c.writer, "\r\033[K")
			c.thinkingCleared = true
			first = false
		}
		fmt.Fprint(c.writer, tok)
	}
	if !first {
		fmt.Fprintln(c.writer)
	}
	return nil
}

// PrintSystem prints a system/info message in dim color.
func (c *CLIChannel) PrintSystem(text string) {
	fmt.Fprintf(c.writer, "%s%s%s\n", colorDim, text, colorReset)
}

// PrintError prints an error message in red, clearing any pending indicator first.
func (c *CLIChannel) PrintError(text string) {
	fmt.Fprintf(c.writer, "\r\033[K%s%s%s\n", colorRed, text, colorReset)
}

// PrintBanner prints the startup banner.
func (c *CLIChannel) PrintBanner(model string, toolCount int, tapePath string) {
	fmt.Fprintf(c.writer, "%sgolem%s v0.1.0\n", colorBold, colorReset)
	fmt.Fprintf(c.writer, "Model: %s\n", model)
	fmt.Fprintf(c.writer, "Tools: %d registered\n", toolCount)
	fmt.Fprintf(c.writer, "Tape:  %s\n", tapePath)
	fmt.Fprintf(c.writer, "Type ,help for commands, ,quit to exit.\n\n")
}
