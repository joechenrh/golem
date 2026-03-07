package tape

import (
	"encoding/json"
	"time"

	"github.com/joechenrh/golem/internal/llm"
)

// EntryKind discriminates tape entry types.
type EntryKind string

const (
	KindEvent    EntryKind = "event"    // system events (session start, config change)
	KindMessage  EntryKind = "message"  // user/assistant messages
	KindAnchor   EntryKind = "anchor"   // context boundary markers
	KindSummary  EntryKind = "summary"  // auto-generated conversation summary
	KindFeedback EntryKind = "feedback" // user satisfaction signals (thumbs up/down)
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
// Only messages after the last anchor are included. Summary entries
// are prepended as system context so restored sessions carry forward
// a condensed history.
func BuildMessages(entries []TapeEntry) []llm.Message {
	// Find last anchor index.
	lastAnchor := -1
	for i, e := range entries {
		if e.Kind == KindAnchor {
			lastAnchor = i
		}
	}

	var msgs []llm.Message

	// Include the most recent summary (if any) as leading context.
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind == KindSummary {
			var payload struct {
				Summary string `json:"summary"`
			}
			if json.Unmarshal(entries[i].Payload, &payload) == nil && payload.Summary != "" {
				msgs = append(msgs, llm.Message{
					Role:    llm.RoleUser,
					Content: "[Previous conversation summary]\n" + payload.Summary,
				})
				msgs = append(msgs, llm.Message{
					Role:    llm.RoleAssistant,
					Content: "Understood, I have the context from our previous conversation.",
				})
			}
			break
		}
	}

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

		// The tape stores image metadata (media_type) without base64
		// data to avoid bloat. Strip these stubs so the LLM API
		// doesn't receive empty image payloads.
		if len(msg.Images) > 0 {
			valid := msg.Images[:0]
			for _, img := range msg.Images {
				if img.Base64 != "" {
					valid = append(valid, img)
				}
			}
			if len(valid) > 0 {
				msg.Images = valid
			} else {
				msg.Images = nil
				if msg.Content == "" {
					msg.Content = "[User sent an image]"
				}
			}
		}

		msgs = append(msgs, msg)
	}

	return msgs
}
