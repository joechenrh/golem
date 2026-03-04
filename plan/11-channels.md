# Step 11: Channel Interface + CLI

## Scope

Define the Channel interface and implement the CLI/REPL channel. Create stubs for Telegram and Lark. Maps to crabclaw's `channels/cli.rs` + `channels/repl.rs`.

## Files

- `internal/channel/channel.go` — Interface + shared types
- `internal/channel/cli/cli.go` — Interactive CLI/REPL
- `internal/channel/telegram/telegram.go` — Stub
- `internal/channel/lark/lark.go` — Stub

## Key Points

### Channel Interface (`channel.go`)

```go
type IncomingMessage struct {
    ChannelID   string            // e.g. "cli", "telegram:12345", "lark:chat_xyz"
    ChannelName string            // "cli", "telegram", "lark"
    SenderID    string
    SenderName  string
    Text        string
    Metadata    map[string]string
}

type OutgoingMessage struct {
    ChannelID string
    Text      string
    Format    string  // "text", "markdown"
}

type Channel interface {
    Name() string
    Start(ctx context.Context, inCh chan<- IncomingMessage) error
    Send(ctx context.Context, msg OutgoingMessage) error
    SendTyping(ctx context.Context, channelID string) error
    SupportsStreaming() bool
    SendStream(ctx context.Context, channelID string, tokenCh <-chan string) error
}
```

### CLI Channel (`cli/cli.go`)

```go
type CLIChannel struct {
    prompt string  // REPL prompt string, e.g. "golem> "
}

func New() *CLIChannel
```

**Start()**: Runs a REPL loop:
1. Print prompt (`golem> `)
2. Read line from stdin (`bufio.Scanner`)
3. If empty, skip
4. Send `IncomingMessage{ChannelID: "cli", Text: line}` to `inCh`
5. Loop until context cancelled or EOF (Ctrl+D)

**Send()**: Print text to stdout with ANSI formatting:
- Assistant responses in default color
- Error messages in red
- System messages in dim/gray

**SendTyping()**: No-op for CLI.

**SupportsStreaming()**: Returns `true`.

**SendStream()**: Reads from `tokenCh`, prints each token to stdout without newline (`fmt.Print`). Prints a newline when channel closes.

### ANSI Color Helpers

```go
const (
    colorReset = "\033[0m"
    colorRed   = "\033[31m"
    colorGreen = "\033[32m"
    colorDim   = "\033[2m"
    colorBold  = "\033[1m"
)
```

Simple ANSI codes — no dependency on a color library.

### Signal Handling

Ctrl+C during input → cancel context → clean exit (handled in main.go, not in CLI channel). CLI channel's `Start()` returns when context is done.

### Telegram Stub (`telegram/telegram.go`)

```go
type TelegramChannel struct{}

func New(cfg *config.Config) *TelegramChannel
func (t *TelegramChannel) Name() string           { return "telegram" }
func (t *TelegramChannel) Start(...) error         { return fmt.Errorf("telegram channel not implemented") }
func (t *TelegramChannel) Send(...) error          { return nil }
func (t *TelegramChannel) SendTyping(...) error    { return nil }
func (t *TelegramChannel) SupportsStreaming() bool  { return false }
func (t *TelegramChannel) SendStream(...) error    { return nil }
```

### Lark Stub (`lark/lark.go`)

Same pattern as Telegram stub.

## Design Decisions

- CLI uses raw `bufio.Scanner` rather than a readline library — keeps dependencies minimal for Phase 1-2. Can upgrade to `github.com/chzyer/readline` later for history/tab completion
- ChannelID includes the channel name as prefix — makes routing unambiguous
- `IncomingMessage.ChannelName` is redundant with ChannelID prefix but convenient for switch statements
- Stubs return errors on `Start()` so accidental use is caught early

## Done When

- CLI channel starts, shows prompt, reads input, sends to inCh
- Typing a message and pressing Enter triggers the agent loop
- Agent response is printed to stdout
- Streaming works — tokens appear one by one
- Ctrl+C / Ctrl+D exits cleanly
- Telegram/Lark stubs compile and implement the interface
