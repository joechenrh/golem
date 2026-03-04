package tape

import (
	"encoding/json"
	"time"

	"github.com/joechenrh/golem/internal/llm"
)

// EntryKind discriminates tape entry types.
type EntryKind string

const (
	KindEvent   EntryKind = "event"   // system events (session start, config change)
	KindMessage EntryKind = "message" // user/assistant messages
	KindAnchor  EntryKind = "anchor"  // context boundary markers
)

// TapeEntry is a single record in the append-only tape.
type TapeEntry struct {
	ID        string                 `json:"id"`
	Kind      EntryKind              `json:"kind"`
	Payload   map[string]interface{} `json:"payload"`
	Timestamp time.Time              `json:"timestamp"`
}

// TapeInfo provides stats about a tape file.
type TapeInfo struct {
	TotalEntries       int    `json:"total_entries"`
	AnchorCount        int    `json:"anchor_count"`
	EntriesSinceAnchor int    `json:"entries_since_anchor"`
	FilePath           string `json:"file_path"`
}

// BuildMessages extracts llm.Message entries from tape entries.
// Only messages after the last anchor are included.
func BuildMessages(entries []TapeEntry) []llm.Message {
	// Find last anchor index.
	lastAnchor := -1
	for i, e := range entries {
		if e.Kind == KindAnchor {
			lastAnchor = i
		}
	}

	var msgs []llm.Message
	for i, e := range entries {
		if i <= lastAnchor {
			continue
		}
		if e.Kind != KindMessage {
			continue
		}

		// Re-marshal the payload and unmarshal into llm.Message
		// to respect JSON tags on both sides.
		data, err := json.Marshal(e.Payload)
		if err != nil {
			continue
		}
		var msg llm.Message
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		msgs = append(msgs, msg)
	}

	return msgs
}
