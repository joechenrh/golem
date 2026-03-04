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
	prompt string
	reader io.Reader
	writer io.Writer
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

		msg := channel.IncomingMessage{
			ChannelID:   "cli",
			ChannelName: "cli",
			Text:        line,
		}

		select {
		case inCh <- msg:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}
	return nil
}

// Send prints a message to the terminal.
func (c *CLIChannel) Send(_ context.Context, msg channel.OutgoingMessage) error {
	fmt.Fprintln(c.writer, msg.Text)
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
	for tok := range tokenCh {
		fmt.Fprint(c.writer, tok)
	}
	fmt.Fprintln(c.writer)
	return nil
}

// PrintSystem prints a system/info message in dim color.
func (c *CLIChannel) PrintSystem(text string) {
	fmt.Fprintf(c.writer, "%s%s%s\n", colorDim, text, colorReset)
}

// PrintError prints an error message in red.
func (c *CLIChannel) PrintError(text string) {
	fmt.Fprintf(c.writer, "%s%s%s\n", colorRed, text, colorReset)
}

// PrintBanner prints the startup banner.
func (c *CLIChannel) PrintBanner(model string, toolCount int, tapePath string) {
	fmt.Fprintf(c.writer, "%sgolem%s v0.1.0\n", colorBold, colorReset)
	fmt.Fprintf(c.writer, "Model: %s\n", model)
	fmt.Fprintf(c.writer, "Tools: %d registered\n", toolCount)
	fmt.Fprintf(c.writer, "Tape:  %s\n", tapePath)
	fmt.Fprintf(c.writer, "Type ,help for commands, ,quit to exit.\n\n")
}
