package tape

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Store is the interface for tape persistence.
type Store interface {
	Append(entry TapeEntry) error
	Entries() ([]TapeEntry, error)
	Search(query string) ([]TapeEntry, error)
	EntriesSince(anchorID string) ([]TapeEntry, error)
	LastAnchor() (*TapeEntry, error)
	AddAnchor(label string) error
	Info() TapeInfo
	Close() error
}

// FileStore is a JSONL-backed tape store with an in-memory cache.
// Entries are loaded from disk once on creation and kept in sync by Append.
// A persistent file handle is used for appends to avoid open/close overhead.
type FileStore struct {
	path    string
	mu      sync.Mutex
	entries []TapeEntry // in-memory cache, authoritative after initial load
	file    *os.File    // persistent append handle
}

// NewFileStore creates or opens a JSONL tape file at the given path.
// If the file already contains entries (session restore), they are loaded
// into the in-memory cache. The file is kept open for appends.
func NewFileStore(path string) (*FileStore, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("tape: open %s: %w", path, err)
	}

	s := &FileStore{path: path, file: f}

	// Load existing entries from disk into the cache.
	entries, err := s.loadFromDisk()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("tape: loading existing entries: %w", err)
	}
	s.entries = entries

	return s, nil
}

func (s *FileStore) Append(entry TapeEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entry.ID == "" {
		entry.ID = uuid.NewString()
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("tape: marshal entry: %w", err)
	}

	if _, err := s.file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("tape: write entry: %w", err)
	}

	// Update in-memory cache after successful disk write.
	s.entries = append(s.entries, entry)
	return nil
}

// Close closes the underlying file handle. Safe to call multiple times.
func (s *FileStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		err := s.file.Close()
		s.file = nil
		return err
	}
	return nil
}

func (s *FileStore) Entries() ([]TapeEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Return a copy to prevent callers from mutating the cache.
	result := make([]TapeEntry, len(s.entries))
	copy(result, s.entries)
	return result, nil
}

func (s *FileStore) Search(query string) ([]TapeEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	lower := strings.ToLower(query)
	var results []TapeEntry
	for _, e := range s.entries {
		if strings.Contains(strings.ToLower(string(e.Payload)), lower) {
			results = append(results, e)
		}
	}
	return results, nil
}

func (s *FileStore) EntriesSince(anchorID string) ([]TapeEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Find the anchor.
	anchorIdx := -1
	for i, e := range s.entries {
		if e.ID == anchorID {
			anchorIdx = i
			break
		}
	}

	if anchorIdx == -1 {
		return nil, fmt.Errorf("tape: anchor %q not found", anchorID)
	}

	// Return entries after the anchor.
	if anchorIdx+1 >= len(s.entries) {
		return nil, nil
	}

	after := s.entries[anchorIdx+1:]
	result := make([]TapeEntry, len(after))
	copy(result, after)
	return result, nil
}

func (s *FileStore) LastAnchor() (*TapeEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := len(s.entries) - 1; i >= 0; i-- {
		if s.entries[i].Kind == KindAnchor {
			e := s.entries[i]
			return &e, nil
		}
	}
	return nil, nil
}

func (s *FileStore) AddAnchor(label string) error {
	return s.Append(TapeEntry{
		Kind:    KindAnchor,
		Payload: MarshalPayload(map[string]any{"label": label}),
	})
}

func (s *FileStore) Info() TapeInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	info := TapeInfo{
		TotalEntries: len(s.entries),
		FilePath:     s.path,
	}

	lastAnchorIdx := -1
	for i, e := range s.entries {
		if e.Kind == KindAnchor {
			info.AnchorCount++
			lastAnchorIdx = i
		}
	}

	if lastAnchorIdx >= 0 {
		info.EntriesSinceAnchor = len(s.entries) - lastAnchorIdx - 1
	} else {
		info.EntriesSinceAnchor = len(s.entries)
	}

	return info
}

// loadFromDisk reads all entries from the JSONL file. Called once during NewFileStore.
func (s *FileStore) loadFromDisk() ([]TapeEntry, error) {
	f, err := os.Open(s.path)
	if err != nil {
		return nil, fmt.Errorf("tape: open for read: %w", err)
	}
	defer f.Close()

	var entries []TapeEntry
	scanner := bufio.NewScanner(f)
	// Increase buffer to 1MB to handle large tape entries (e.g. verbose tool outputs).
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var entry TapeEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			// Graceful recovery: skip invalid lines.
			continue
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("tape: scan: %w", err)
	}

	return entries, nil
}
