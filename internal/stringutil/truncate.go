package stringutil

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
