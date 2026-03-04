# Step 5: Tape Store

## Scope

Append-only JSONL conversation history with anchors and search. Maps to crabclaw's `tape/store.rs`.

## Files

- `internal/tape/entry.go` — TapeEntry and EntryKind types
- `internal/tape/store.go` — FileStore implementation

## Key Points

### Data Model (`entry.go`)

```go
type EntryKind string
const (
    KindEvent   EntryKind = "event"    // system events (session start, config change)
    KindMessage EntryKind = "message"  // user/assistant messages
    KindAnchor  EntryKind = "anchor"   // context boundary markers
)

type TapeEntry struct {
    ID        string                 `json:"id"`        // UUID
    Kind      EntryKind              `json:"kind"`
    Payload   map[string]interface{} `json:"payload"`   // flexible JSON payload
    Timestamp time.Time              `json:"timestamp"`
}
```

**Payload conventions** (not enforced by type, but by usage):
- Message entries: `{"role": "user"|"assistant"|"tool", "content": "...", "tool_calls": [...]}`
- Event entries: `{"type": "session_start"|"command", "data": "..."}`
- Anchor entries: `{"label": "session-start"|"context-reset"}`

### Store Interface and Implementation (`store.go`)

```go
type Store interface {
    Append(entry TapeEntry) error
    Entries() ([]TapeEntry, error)
    Search(query string) ([]TapeEntry, error)         // case-insensitive substring search
    EntriesSince(anchorID string) ([]TapeEntry, error) // entries after a specific anchor
    LastAnchor() (*TapeEntry, error)                   // most recent anchor
    AddAnchor(label string) error                      // convenience: appends an anchor entry
    Info() TapeInfo                                    // stats: total entries, anchors, entries since last anchor
}

type TapeInfo struct {
    TotalEntries      int
    AnchorCount       int
    EntriesSinceAnchor int
    FilePath          string
}
```

### FileStore Implementation

- One JSONL file per session/channel (path determined by channel ID)
- Each `Append()` writes one JSON line + newline, with `os.O_APPEND|os.O_CREATE`
- `Entries()` reads all lines, skips invalid JSON (graceful recovery)
- `Search()` does case-insensitive substring match on serialized payload
- `EntriesSince()` returns entries after the specified anchor ID
- Thread-safe via `sync.Mutex`
- IDs generated via `github.com/google/uuid`

### File Format

One JSON object per line:
```json
{"id":"abc-123","kind":"message","payload":{"role":"user","content":"hello"},"timestamp":"2026-03-04T10:00:00Z"}
{"id":"abc-124","kind":"message","payload":{"role":"assistant","content":"Hi!"},"timestamp":"2026-03-04T10:00:01Z"}
{"id":"abc-125","kind":"anchor","payload":{"label":"context-reset"},"timestamp":"2026-03-04T10:05:00Z"}
```

### Context Building Helper

```go
// BuildMessages extracts llm.Message entries from the tape, optionally since the last anchor.
// Used by the agent loop to construct the conversation history for LLM calls.
func BuildMessages(entries []TapeEntry) []llm.Message
```

This converts tape entries with `kind=message` into `llm.Message` structs. Entries with `kind=anchor` are used as context boundaries — only messages after the last anchor are included.

## Implementation Notes

- All struct fields must have `json:` tags — this was a review finding from Step 03. Without tags, JSONL serialization uses PascalCase field names which is inconsistent with the rest of the system.
- `BuildMessages` converts tape entries to `llm.Message`. Since `llm.Message` already has JSON tags, ensure the payload parsing respects the `json:"snake_case"` field names.
- Ensure tests cover JSON round-tripping (marshal → unmarshal), not just in-memory operations.

## Design Decisions

- Payload is `map[string]interface{}` (not a typed union) — keeps the tape format flexible and forward-compatible
- File-per-session avoids concurrency issues; different channels get different tape files
- No in-memory cache of entries — always read from file. Tape files are small (a few MB max per session)
- Anchors are a lightweight mechanism — just a special entry, no separate tracking

## Done When

- `NewFileStore(path)` creates/opens a JSONL file
- `Append()` + `Entries()` round-trips correctly
- `AddAnchor("test")` + `EntriesSince(anchorID)` returns only entries after anchor
- `Search("hello")` finds entries containing "hello" in payload
- `Info()` returns correct counts
