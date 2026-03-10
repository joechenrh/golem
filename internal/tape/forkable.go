package tape

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ForkableStore wraps a Store and buffers Append calls in memory.
// Call Commit to flush buffered entries to the underlying store,
// or Rollback to discard them. Mid-turn reads (Entries, Search, etc.)
// see parent entries combined with pending entries, so buffered writes
// are visible within the same turn.
type ForkableStore struct {
	parent  Store
	pending []TapeEntry
}

// Fork creates a ForkableStore that buffers writes above the given parent.
func Fork(parent Store) *ForkableStore {
	return &ForkableStore{parent: parent}
}

// Append buffers the entry in memory without writing to the parent store.
func (f *ForkableStore) Append(entry TapeEntry) error {
	if entry.ID == "" {
		entry.ID = uuid.NewString()
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	f.pending = append(f.pending, entry)
	return nil
}

// Entries returns parent entries followed by pending entries.
func (f *ForkableStore) Entries() ([]TapeEntry, error) {
	parent, err := f.parent.Entries()
	if err != nil {
		return nil, err
	}
	if len(f.pending) == 0 {
		return parent, nil
	}
	// Build a combined slice without mutating the parent's backing array.
	combined := make([]TapeEntry, 0, len(parent)+len(f.pending))
	combined = append(combined, parent...)
	combined = append(combined, f.pending...)
	return combined, nil
}

// Search returns entries from both parent and pending that match the query.
func (f *ForkableStore) Search(query string) ([]TapeEntry, error) {
	parentResults, err := f.parent.Search(query)
	if err != nil {
		return nil, err
	}
	lower := strings.ToLower(query)
	for _, e := range f.pending {
		if strings.Contains(strings.ToLower(string(e.Payload)), lower) {
			parentResults = append(parentResults, e)
		}
	}
	return parentResults, nil
}

// EntriesSince returns entries after the given anchor ID, searching both
// parent and pending entries.
func (f *ForkableStore) EntriesSince(anchorID string) ([]TapeEntry, error) {
	all, err := f.Entries()
	if err != nil {
		return nil, err
	}
	anchorIdx := -1
	for i, e := range all {
		if e.ID == anchorID {
			anchorIdx = i
			break
		}
	}
	if anchorIdx == -1 {
		return nil, fmt.Errorf("tape: anchor %q not found", anchorID)
	}
	if anchorIdx+1 >= len(all) {
		return nil, nil
	}
	// Return a copy so callers can't mutate our combined slice.
	result := make([]TapeEntry, len(all[anchorIdx+1:]))
	copy(result, all[anchorIdx+1:])
	return result, nil
}

// LastAnchor returns the most recent anchor, checking pending first
// (reverse order), then falling back to the parent.
func (f *ForkableStore) LastAnchor() (*TapeEntry, error) {
	for i := len(f.pending) - 1; i >= 0; i-- {
		if f.pending[i].Kind == KindAnchor {
			e := f.pending[i]
			return &e, nil
		}
	}
	return f.parent.LastAnchor()
}

// AddAnchor buffers an anchor entry.
func (f *ForkableStore) AddAnchor(label string) error {
	return f.Append(TapeEntry{
		Kind:    KindAnchor,
		Payload: MarshalPayload(map[string]any{"label": label}),
	})
}

// Info returns combined stats from parent and pending entries.
func (f *ForkableStore) Info() TapeInfo {
	info := f.parent.Info()
	info.TotalEntries += len(f.pending)

	// Recount anchors and entries-since-anchor across the combined view.
	pendingAnchors := 0
	lastPendingAnchorIdx := -1
	for i, e := range f.pending {
		if e.Kind == KindAnchor {
			pendingAnchors++
			lastPendingAnchorIdx = i
		}
	}
	info.AnchorCount += pendingAnchors

	if lastPendingAnchorIdx >= 0 {
		// Last anchor is in pending; entries since = remaining pending after it.
		info.EntriesSinceAnchor = len(f.pending) - lastPendingAnchorIdx - 1
	} else {
		// No anchor in pending; add pending count to parent's entries-since-anchor.
		info.EntriesSinceAnchor += len(f.pending)
	}

	return info
}

// Close delegates to the parent store. Pending entries are discarded.
func (f *ForkableStore) Close() error {
	return f.parent.Close()
}

// Commit flushes all pending entries to the parent store in order.
func (f *ForkableStore) Commit() error {
	for _, entry := range f.pending {
		if err := f.parent.Append(entry); err != nil {
			return fmt.Errorf("tape fork commit: %w", err)
		}
	}
	f.pending = nil
	return nil
}

// Rollback discards all pending entries.
func (f *ForkableStore) Rollback() {
	f.pending = nil
}

// Pending returns the number of buffered entries.
func (f *ForkableStore) Pending() int {
	return len(f.pending)
}
