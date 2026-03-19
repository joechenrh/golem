package stringutil

import "fmt"

// Truncate returns s truncated to maxLen characters with a "..." suffix.
// If s is already within maxLen, it is returned unchanged.
func Truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// TruncateWithNote returns s truncated to maxLen characters with a
// "\n... [truncated]" suffix, suitable for command output.
func TruncateWithNote(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "\n... [truncated]"
}

// TruncateHeadTail returns s truncated to approximately maxBytes by keeping
// the head and tail, with a note in the middle showing how many bytes were
// removed. The total output (head + note + tail) fits within maxBytes.
// Falls back to TruncateWithNote if maxBytes is too small for the note.
func TruncateHeadTail(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	removed := len(s) - maxBytes
	note := fmt.Sprintf("\n... [truncated %d bytes] ...\n", removed)
	if len(note) >= maxBytes {
		return TruncateWithNote(s, maxBytes)
	}
	headBytes := (maxBytes - len(note)) / 2
	tailBytes := maxBytes - len(note) - headBytes
	return s[:headBytes] + note + s[len(s)-tailBytes:]
}
