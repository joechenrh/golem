package tape

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"slices"
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

// MaxTapeFileSize is the default tape rotation threshold (50 MB).
const MaxTapeFileSize int64 = 50 * 1024 * 1024

// FileStore is a JSONL-backed tape store with an in-memory cache.
// Entries are loaded from disk once on creation and kept in sync by Append.
// A persistent file handle is used for appends to avoid open/close overhead.
type FileStore struct {
	path      string
	mu        sync.Mutex
	entries   []TapeEntry // in-memory cache, authoritative after initial load
	file      *os.File    // persistent append handle
	diskBytes int64       // current file size on disk
}

// NewFileStore creates or opens a JSONL tape file at the given path.
// If the file already contains entries (session restore), they are loaded
// into the in-memory cache. The file handle is opened lazily on the first
// Append call, so sessions that never write avoid creating empty files.
func NewFileStore(path string) (*FileStore, error) {
	s := &FileStore{path: path}

	// Load existing entries from disk if the file exists.
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		entries, err := s.loadFromDisk()
		if err != nil {
			return nil, fmt.Errorf("tape: loading existing entries: %w", err)
		}
		s.entries = entries
		s.diskBytes = info.Size()
	}

	return s, nil
}

// ensureOpen opens the file handle for appends if not already open.
// Must be called with s.mu held.
func (s *FileStore) ensureOpen() error {
	if s.file != nil {
		return nil
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("tape: open %s: %w", s.path, err)
	}
	s.file = f
	return nil
}

func (s *FileStore) Append(entry TapeEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureOpen(); err != nil {
		return err
	}

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

	line := append(data[:len(data):len(data)], '\n')
	if _, err := s.file.Write(line); err != nil {
		return fmt.Errorf("tape: write entry: %w", err)
	}
	if err := s.file.Sync(); err != nil {
		return fmt.Errorf("tape: fsync: %w", err)
	}
	s.diskBytes += int64(len(line))

	// Update in-memory cache after successful disk write.
	s.entries = append(s.entries, entry)

	// Rotate if the file exceeds the size limit.
	if s.diskBytes >= MaxTapeFileSize {
		s.rotateLocked()
	}

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

	// Return a snapshot backed by the same underlying array. Callers must
	// treat the returned slice as read-only. This avoids an O(n) copy on
	// every ReAct iteration. Append only extends the slice, so existing
	// indices remain stable.
	return s.entries[:len(s.entries):len(s.entries)], nil
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

	return slices.Clone(s.entries[anchorIdx+1:]), nil
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

// rotateLocked renames the current tape to a .bak file and starts fresh.
// Retained entries (from the last anchor onward) are written to the new
// file so they survive a crash after rotation.
// Must be called with s.mu held.
func (s *FileStore) rotateLocked() {
	// Close the current file handle.
	s.file.Close()

	// Rename to .bak (overwrites any previous backup).
	backupPath := s.path + ".bak"
	os.Rename(s.path, backupPath)

	// Open a fresh file. On failure, reopen the backup to avoid data loss.
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		os.Rename(backupPath, s.path)
		f, _ = os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	}
	s.file = f

	// Keep only the last anchor and entries after it in memory.
	s.entries = s.entriesFromLastAnchor()

	// Persist retained entries to the new file so they survive a crash.
	s.diskBytes = 0
	for _, e := range s.entries {
		if data, err := json.Marshal(e); err == nil {
			line := append(data, '\n')
			s.file.Write(line)
			s.diskBytes += int64(len(line))
		}
	}
}

// entriesFromLastAnchor returns entries from the last anchor onward,
// or the most recent 100 entries if no anchor exists.
func (s *FileStore) entriesFromLastAnchor() []TapeEntry {
	for i := len(s.entries) - 1; i >= 0; i-- {
		if s.entries[i].Kind == KindAnchor {
			return s.entries[i:]
		}
	}
	// No anchor — keep the tail.
	if len(s.entries) > 100 {
		return s.entries[len(s.entries)-100:]
	}
	return s.entries
}
