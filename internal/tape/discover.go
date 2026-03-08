package tape

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// AgentDir returns the per-agent tape directory under tapeDir, creating it if needed.
func AgentDir(tapeDir, agentName string) (string, error) {
	dir := filepath.Join(tapeDir, agentName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating agent tape dir: %w", err)
	}
	return dir, nil
}

// Discover returns existing tape file paths in dir that match the given prefix.
// Results are sorted by name (which includes timestamps, so naturally chronological).
func Discover(dir, prefix string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var paths []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".jsonl") {
			paths = append(paths, filepath.Join(dir, name))
		}
	}
	slices.Sort(paths)
	return paths, nil
}

// ParseChatID extracts the chat ID from a tape filename.
// Expected format: session-<chatID>-<timestamp>.jsonl
// prefix should be "session-".
func ParseChatID(filename, prefix string) string {
	// Strip prefix.
	rest := strings.TrimPrefix(filename, prefix)
	if rest == filename {
		return "" // prefix didn't match
	}
	// Strip .jsonl suffix.
	rest = strings.TrimSuffix(rest, ".jsonl")
	if rest == "" {
		return ""
	}

	// The remainder is "<chatID>-<timestamp>".
	// Timestamp format is "20060102-150405" (15 chars).
	// Find the last dash that's preceded by exactly a YYYYMMDD pattern.
	// We look for the second-to-last dash since timestamp has one dash too.
	lastDash := strings.LastIndex(rest, "-")
	if lastDash <= 0 {
		return ""
	}
	// The timestamp portion is "YYYYMMDD-HHMMSS", so we need to find
	// the dash before that (the one separating chatID from timestamp).
	beforeLast := rest[:lastDash]
	secondLastDash := strings.LastIndex(beforeLast, "-")
	if secondLastDash <= 0 {
		return ""
	}
	return rest[:secondLastDash]
}

// ExtractLastSummary reads a tape file and returns the content of the last
// KindSummary entry, or "" if none exists. The file is opened read-only
// and scanned line by line to avoid loading the entire tape into memory.
func ExtractLastSummary(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	var lastSummary string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var entry TapeEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry.Kind != KindSummary {
			continue
		}
		var payload struct {
			Summary string `json:"summary"`
		}
		if json.Unmarshal(entry.Payload, &payload) == nil && payload.Summary != "" {
			lastSummary = payload.Summary
		}
	}
	return lastSummary
}
