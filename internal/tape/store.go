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
}

// FileStore is a JSONL-backed tape store.
type FileStore struct {
	path string
	mu   sync.Mutex
}

// NewFileStore creates or opens a JSONL tape file at the given path.
func NewFileStore(path string) (*FileStore, error) {
	// Ensure the file exists.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("tape: open %s: %w", path, err)
	}
	f.Close()

	return &FileStore{path: path}, nil
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

	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("tape: open for append: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("tape: write entry: %w", err)
	}
	return nil
}

func (s *FileStore) Entries() ([]TapeEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.readEntries()
}

func (s *FileStore) Search(query string) ([]TapeEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.readEntries()
	if err != nil {
		return nil, err
	}

	lower := strings.ToLower(query)
	var results []TapeEntry
	for _, e := range entries {
		data, err := json.Marshal(e.Payload)
		if err != nil {
			continue
		}
		if strings.Contains(strings.ToLower(string(data)), lower) {
			results = append(results, e)
		}
	}
	return results, nil
}

func (s *FileStore) EntriesSince(anchorID string) ([]TapeEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.readEntries()
	if err != nil {
		return nil, err
	}

	// Find the anchor.
	anchorIdx := -1
	for i, e := range entries {
		if e.ID == anchorID {
			anchorIdx = i
			break
		}
	}

	if anchorIdx == -1 {
		return nil, fmt.Errorf("tape: anchor %q not found", anchorID)
	}

	// Return entries after the anchor.
	if anchorIdx+1 >= len(entries) {
		return nil, nil
	}
	return entries[anchorIdx+1:], nil
}

func (s *FileStore) LastAnchor() (*TapeEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.readEntries()
	if err != nil {
		return nil, err
	}

	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind == KindAnchor {
			return &entries[i], nil
		}
	}
	return nil, nil
}

func (s *FileStore) AddAnchor(label string) error {
	return s.Append(TapeEntry{
		Kind: KindAnchor,
		Payload: map[string]interface{}{
			"label": label,
		},
	})
}

func (s *FileStore) Info() TapeInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.readEntries()
	if err != nil {
		return TapeInfo{FilePath: s.path}
	}

	info := TapeInfo{
		TotalEntries: len(entries),
		FilePath:     s.path,
	}

	lastAnchorIdx := -1
	for i, e := range entries {
		if e.Kind == KindAnchor {
			info.AnchorCount++
			lastAnchorIdx = i
		}
	}

	if lastAnchorIdx >= 0 {
		info.EntriesSinceAnchor = len(entries) - lastAnchorIdx - 1
	} else {
		info.EntriesSinceAnchor = len(entries)
	}

	return info
}

// readEntries reads all entries from the JSONL file. Must be called with mu held.
func (s *FileStore) readEntries() ([]TapeEntry, error) {
	f, err := os.Open(s.path)
	if err != nil {
		return nil, fmt.Errorf("tape: open for read: %w", err)
	}
	defer f.Close()

	var entries []TapeEntry
	scanner := bufio.NewScanner(f)
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
