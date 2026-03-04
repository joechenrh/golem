# Step 7: Router

## Scope

Input routing — detect comma commands in user input and assistant output, skip commands inside code fences. Maps to crabclaw's `core/router.rs` + `core/command.rs`.

## File

`internal/router/router.go`

## Key Points

### User Input Routing

```go
type RouteResult struct {
    IsCommand bool
    Command   string   // command name (e.g., "help", "tape.info", "git status")
    Args      string   // everything after the command name
    Kind      CommandKind  // Internal or Shell
}

type CommandKind int
const (
    CommandInternal CommandKind = iota  // ,help, ,tape.info, ,tools, ,quit, ,model
    CommandShell                        // ,git status, ,ls -la
)

// RouteUser classifies user input.
// Lines starting with "," are commands; everything else goes to the LLM.
func RouteUser(input string) RouteResult
```

**Routing rules** (matching crabclaw):
- Input starting with `,` followed by a known internal command → `CommandInternal`
- Input starting with `,` followed by anything else → `CommandShell` (treat as shell command)
- All other input → `IsCommand: false` (send to LLM)

### Internal Commands

| Command | Description |
|---|---|
| `,help` | List available commands |
| `,quit` | Exit the agent |
| `,tape.info` | Show tape statistics |
| `,tape.search <query>` | Search tape history |
| `,tools` | List registered tools |
| `,skills` | List discovered skills |
| `,model [name]` | Show or change current model |
| `,anchor [label]` | Add a tape anchor |

### Assistant Output Routing

```go
type DetectedCommand struct {
    Command string
    Args    string
    Kind    CommandKind
    Line    int  // line number in output where command was found
}

// RouteAssistant scans assistant output for comma commands.
// Skips commands inside code fences (``` blocks).
// Returns detected commands and the cleaned text (commands removed).
func RouteAssistant(output string) (commands []DetectedCommand, cleanText string)
```

**Code fence detection**: Track whether we're inside a ``` block. Any `,command` inside a code fence is NOT treated as a command — it's just code content.

### Command Parsing

```go
// ParseArgs splits command arguments into positional args and flags.
func ParseArgs(raw string) ParsedArgs

type ParsedArgs struct {
    Positional []string
    Flags      map[string]string  // --key=value or --key value
    BoolFlags  map[string]bool    // --flag (no value)
}
```

## Design Decisions

- Comma prefix (`,`) follows bub/crabclaw convention — distinct from slash commands used by other tools
- Internal command names are hardcoded, not pluggable (keeps router simple)
- Code fence tracking is line-by-line — handles nested fences by counting open/close
- Assistant command detection is conservative — only exact `,command` at line start

## Done When

- `RouteUser(",help")` → `{IsCommand: true, Command: "help", Kind: CommandInternal}`
- `RouteUser(",git status")` → `{IsCommand: true, Command: "git status", Kind: CommandShell}`
- `RouteUser("What is Go?")` → `{IsCommand: false}`
- `RouteAssistant("Here's code:\n```\n,ls\n```\n,git log")` → detects only `,git log`, not `,ls`
