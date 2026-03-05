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
// Payload is stored as json.RawMessage to preserve type fidelity across
// JSON round-trips (e.g., []ToolCall survives serialization to JSONL and back).
type TapeEntry struct {
	ID        string          `json:"id"`
	Kind      EntryKind       `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
	Timestamp time.Time       `json:"timestamp"`
}

// MarshalPayload marshals v to json.RawMessage for use as a TapeEntry payload.
func MarshalPayload(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return data
}

// PayloadMap unmarshals the payload into a map for ad-hoc field access.
func (e TapeEntry) PayloadMap() map[string]any {
	var m map[string]any
	if err := json.Unmarshal(e.Payload, &m); err != nil {
		return nil
	}
	return m
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

		// Unmarshal directly from the raw JSON payload — no re-marshaling
		// needed since Payload is json.RawMessage.
		var msg llm.Message
		if err := json.Unmarshal(e.Payload, &msg); err != nil {
			continue
		}

		// For user messages with sender info, prepend [sender:xxx] so the
		// LLM can distinguish speakers in group chats.
		if msg.Role == llm.RoleUser {
			p := e.PayloadMap()
			if senderID, _ := p["sender_id"].(string); senderID != "" {
				msg.Content = "[sender:" + senderID + "] " + msg.Content
			}
		}

		msgs = append(msgs, msg)
	}

	return msgs
}
